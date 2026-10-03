# Mailcore — Prototype Plan

Dokumen ini menjelaskan rancangan lengkap **Mailcore**: layanan mail berbasis Go yang
menjadi *core API* pengiriman email untuk seluruh aplikasi (Simpora panel/core dan
aplikasi berikutnya).

---

## 1. Tujuan

| # | Tujuan | Keterangan |
|---|--------|------------|
| 1 | **Satu pintu pengiriman email** | Semua aplikasi cukup memanggil HTTP API, tidak perlu library SMTP masing-masing. |
| 2 | **Credential SMTP dari aplikasi** | Aplikasi klien mengirim `smtp` (host, port, username, password, enkripsi) di setiap request. Mailcore tidak terikat ke satu provider (Gmail, Office 365, Mailtrap, SES SMTP, server sendiri, dll). |
| 3 | **Single & Bulk** | `POST /v1/send` untuk satu email (sinkron), `POST /v1/send/bulk` untuk banyak penerima (asinkron, dipersonalisasi per penerima). |
| 4 | **Aman by default** | API key, tidak menyimpan/log password, anti header-injection, TLS wajib untuk auth. |
| 5 | **Ringan** | Satu binary Go tanpa dependensi eksternal, image Docker ±10 MB. |

**Bukan tujuan prototype:** menjadi mail server penerima (MX/IMAP), mengelola DNS/DKIM
domain, atau dashboard UI. Itu masuk roadmap (lihat §10).

---

## 2. Arsitektur

```
┌────────────────────┐   HTTPS + API key    ┌──────────────────────────────────────────────┐
│ Aplikasi klien     │ ───────────────────▶ │ Mailcore (Go)                                │
│ (Laravel, dll)     │                      │                                              │
│                    │ ◀─ 200 / 202 + JSON ─│  ┌─────────┐   ┌──────────┐   ┌────────────┐ │
│                    │                      │  │ HTTP API│──▶│ Validasi │──▶│ MIME       │ │
│                    │                      │  │ + Auth  │   │ & Render │   │ Builder    │ │
│                    │                      │  └─────────┘   └──────────┘   └─────┬──────┘ │
│                    │                      │       │ bulk                        │        │
│                    │                      │       ▼                             ▼        │
│                    │   callback (HMAC)    │  ┌──────────────┐  worker pool ┌──────────┐ │
│                    │ ◀─────────────────── │  │ Job Manager  │─────────────▶│ SMTP     │─┼──▶ SMTP server
└────────────────────┘                      │  │ queue + TTL  │  rate limit  │ Client   │ │    (Gmail/O365/…)
                                            │  └──────────────┘  retry      └──────────┘ │
                                            └──────────────────────────────────────────────┘
```

### Komponen (folder `internal/`)

| Paket | Tanggung jawab |
|-------|----------------|
| `mailer` | Model `Address`, `Message`, `Attachment`; validasi; builder MIME (RFC 5322, multipart/alternative + mixed, quoted-printable, base64); klien SMTP (`ssl`/`starttls`/`none`, AUTH PLAIN/LOGIN, reuse koneksi); klasifikasi error permanen vs sementara; template per penerima. |
| `job` | Job bulk: antrian in-memory, N runner job, worker pool per job (1 koneksi SMTP per worker), rate limit, retry + exponential backoff, cancel, callback ber-signature HMAC, kedaluwarsa job (TTL). |
| `api` | Endpoint HTTP/JSON, auth API key, logging request, recover panic, batas ukuran body, mapping error ke kode HTTP. |
| `config` | Konfigurasi via environment variable. |
| `smtptest` | SMTP server palsu in-process untuk test end-to-end. |

---

## 3. Alur Kerja

### 3.1 Single send — `POST /v1/send` (sinkron)

1. Auth API key → decode JSON (field tak dikenal ditolak agar typo ketahuan).
2. Validasi `smtp`, alamat, subject/header (anti CR/LF injection), lampiran, batas jumlah penerima.
3. Build MIME + `Message-ID` unik.
4. Dial SMTP → (STARTTLS) → AUTH → `MAIL FROM` → `RCPT TO` (to+cc+bcc) → `DATA`.
5. Respons `200` berisi `message_id`, `accepted`, dan `rejected` (bila sebagian penerima ditolak → `status: "partial"`).
   Gagal di SMTP → `502` dengan `stage` (connect/tls/auth/rcpt_to/data) supaya klien tahu salahnya di mana.

### 3.2 Bulk send — `POST /v1/send/bulk` (asinkron)

1. Validasi **seluruh** penerima di depan (alamat, render template, header). Jika ada yang salah →
   `422` + daftar `details`, **tidak ada** yang dikirim. Duplikat email dibuang otomatis.
2. Job masuk antrian → respons `202 Accepted` + `job_id` (+ header `Location`).
3. Runner mengambil job → membuka `concurrency` worker. Setiap worker:
   - membuka **satu** koneksi SMTP dan memakainya untuk banyak email (hemat handshake TLS/AUTH),
   - menunggu token rate limit (`rate_per_second`, dibagi rata ke semua worker),
   - render subject/text/html dengan data penerima, kirim **1 email per penerima** (alamat penerima lain tidak terlihat),
   - error **sementara** (4xx, jaringan, timeout) → reconnect + retry dengan backoff (2s, 4s, 8s … maks 30s),
   - error **permanen** (5xx, mis. mailbox tidak ada) → penerima ditandai `failed`, lanjut ke berikutnya,
   - **auth gagal** → seluruh job dihentikan (`status: failed`) karena pasti gagal semua.
4. Klien memantau via `GET /v1/jobs/{id}` (polling) **atau** menerima `callback_url` saat job selesai.
5. Setelah selesai, credential SMTP, lampiran, dan data template dihapus dari memori; ringkasan & hasil per penerima
   tetap bisa diambil selama `MAILCORE_JOB_TTL` (default 24 jam).

### 3.3 Status

| Job | Arti |
|-----|------|
| `queued` | Menunggu runner. |
| `running` | Sedang mengirim. |
| `completed` | Semua penerima sudah diproses (cek `sent`/`failed`). |
| `failed` | Dihentikan karena error fatal (mis. auth SMTP salah). |
| `cancelled` | Dibatalkan klien (`DELETE /v1/jobs/{id}`) atau server shutdown. |

Penerima: `pending` → `sent` / `failed` / `cancelled`, lengkap dengan `attempts`, `message_id`, `error`, `sent_at`.

---

## 4. Kontrak API (ringkas)

Detail lengkap + contoh: [`docs/API.md`](API.md).

| Method | Path | Fungsi |
|--------|------|--------|
| `GET` | `/healthz` | Health check (tanpa auth). |
| `POST` | `/v1/send` | Kirim satu email (sinkron). |
| `POST` | `/v1/send/bulk` | Buat job bulk (asinkron). |
| `GET` | `/v1/jobs` | Daftar job (ringkasan). |
| `GET` | `/v1/jobs/{id}` | Progres + hasil per penerima (`?results=false` untuk ringkasan saja). |
| `DELETE` | `/v1/jobs/{id}` | Batalkan job. |
| `POST` | `/v1/smtp/verify` | Tes koneksi + login SMTP tanpa mengirim email. |

Blok `smtp` yang dikirim aplikasi:

```json
{
  "host": "smtp.gmail.com",
  "port": 587,
  "username": "noreply@domainanda.id",
  "password": "app-password",
  "encryption": "starttls",
  "auth": "auto"
}
```

`encryption`: `ssl` (465), `starttls` (587, default), `none` (relay internal/testing).
`auth`: `auto` (PLAIN bila ada, lalu LOGIN), `plain`, `login`, `none`.

---

## 5. Keamanan

| Risiko | Mitigasi di prototype |
|--------|-----------------------|
| Akses tanpa izin | API key (`Authorization: Bearer` / `X-API-Key`), dibandingkan constant-time. Server memberi peringatan keras bila key kosong. |
| Bocornya password SMTP | Tidak pernah di-log, tidak ikut di respons job, dihapus dari memori setelah job selesai, tidak disimpan ke disk. |
| Password terkirim plaintext | AUTH ditolak di koneksi tanpa TLS kecuali `localhost` atau klien eksplisit set `allow_insecure_auth`. TLS minimal 1.2. |
| Header injection (`\r\nBcc: …`) | Semua subject, nama, header custom, filename divalidasi bebas CR/LF; header inti (From, To, Subject, …) tidak bisa ditimpa. |
| HTML injection via data template | Template `html` memakai `html/template` → data penerima di-escape otomatis. |
| Bcc bocor | Bcc hanya di envelope SMTP, tidak ditulis di header. Bulk mengirim 1 email per penerima. |
| Payload raksasa / DoS | Batas body (25 MB), batas penerima per email & per bulk, batas concurrency & retry, antrian berukuran tetap (`503` + `Retry-After` saat penuh), timeout HTTP & SMTP. |
| Callback dipalsukan | Callback ditandatangani `X-Mailcore-Signature: sha256=<HMAC body>` dengan `MAILCORE_CALLBACK_SECRET`. |
| Akses jaringan | Jalankan di jaringan privat / di belakang reverse proxy HTTPS; hanya aplikasi internal yang boleh memanggil. |

> **Catatan deploy:** Mailcore harus berjalan di belakang HTTPS (Nginx/Caddy/Traefik), karena request membawa password SMTP.

---

## 6. Konfigurasi

Semua via environment variable — lihat [`.env.example`](../.env.example).

| Variabel | Default | Fungsi |
|----------|---------|--------|
| `MAILCORE_ADDR` | `:8080` | Alamat HTTP. |
| `MAILCORE_API_KEYS` | – | API key, pisahkan koma (1 key per aplikasi disarankan). |
| `MAILCORE_MAX_BODY_BYTES` | 25 MB | Batas ukuran request. |
| `MAILCORE_MAX_RECIPIENTS_PER_EMAIL` | 50 | to+cc+bcc untuk `/v1/send`. |
| `MAILCORE_MAX_BULK_RECIPIENTS` | 5000 | Penerima per job bulk. |
| `MAILCORE_MAX_BULK_CONCURRENCY` | 10 | Batas `options.concurrency`. |
| `MAILCORE_MAX_RETRIES` | 5 | Batas `options.max_retries`. |
| `MAILCORE_JOB_WORKERS` | 4 | Jumlah job bulk yang berjalan paralel. |
| `MAILCORE_JOB_QUEUE_SIZE` | 100 | Kapasitas antrian job. |
| `MAILCORE_JOB_TTL` | `24h` | Lama hasil job disimpan. |
| `MAILCORE_SMTP_TIMEOUT` | `30s` | Timeout connect/auth dan per email. |
| `MAILCORE_HELO_NAME` | hostname | Nama di `EHLO`. |
| `MAILCORE_CALLBACK_SECRET` | – | Kunci HMAC callback. |

---

## 7. Strategi Pengujian

- **Unit test** builder MIME (multipart, encoding UTF-8, Bcc tidak bocor, lampiran), validasi anti-injection, template.
- **Integrasi** dengan `internal/smtptest` (SMTP server palsu in-process): AUTH PLAIN/LOGIN, penolakan RCPT sebagian,
  dot-stuffing, reuse koneksi, retry 4xx, gagal permanen 5xx, abort saat auth salah, cancel, callback + HMAC.
- **End-to-end HTTP** dengan `httptest`: auth, validasi, single, bulk sampai selesai.
- Semua test dijalankan dengan `-race` di CI (GitHub Actions).
- **Manual:** `docker compose --profile dev up` lalu kirim ke Mailpit (`host: mailpit`, `port: 1025`, `encryption: none`) dan lihat di `http://localhost:8025`.

---

## 8. Batasan Prototype (disadari)

1. **Antrian in-memory** — job yang sedang berjalan/antre hilang bila proses restart (job ditandai `cancelled` saat shutdown normal). Cukup untuk 1 instance.
2. **Tidak horizontal-scalable** — status job ada di memori satu instance.
3. **Credential dikirim tiap request** — praktis, tetapi aplikasi harus menyimpan password SMTP sendiri.
4. **Tidak ada idempotency key** — retry dari sisi klien untuk `/v1/send` bisa mengirim dua kali.
5. **Bounce/complaint tidak dilacak** — yang tercatat hanya respons SMTP saat kirim.
6. **Belum ada inline image (CID), DKIM signing, atau template tersimpan.**

---

## 9. Tahapan Implementasi

| Fase | Isi | Status |
|------|-----|--------|
| **0. Fondasi** | Struktur repo, config env, logging JSON, Dockerfile, CI. | ✅ selesai |
| **1. Mailer core** | Address/Message/MIME builder, SMTP client (ssl/starttls/none, PLAIN/LOGIN), klasifikasi error. | ✅ selesai |
| **2. Single API** | `/v1/send`, `/v1/smtp/verify`, auth API key, validasi, error terstruktur. | ✅ selesai |
| **3. Bulk engine** | Job manager, worker pool, reuse koneksi, rate limit, retry/backoff, cancel, callback HMAC, TTL. | ✅ selesai |
| **4. Uji & dokumentasi** | Test unit/integrasi/E2E dengan SMTP palsu, dokumen API, contoh klien Laravel. | ✅ selesai |
| **5. Integrasi Simpora** | Pasang `MailcoreClient` di `simpora2026-core`, ganti pengiriman email lama, uji dengan Mailpit lalu SMTP production. | ⏭ berikutnya |

---

## 10. Roadmap Setelah Prototype

**v0.2 — Produksi dasar**
- Antrian persisten (Redis Streams atau PostgreSQL `SKIP LOCKED`) agar job tahan restart & bisa multi-instance.
- **SMTP Profile tersimpan**: `POST /v1/smtp-profiles` → credential dienkripsi (AES-GCM, kunci dari env/KMS), aplikasi cukup kirim `"smtp_profile": "simpora-noreply"`.
- Header `Idempotency-Key` untuk `/v1/send` dan `/v1/send/bulk`.
- Metrics Prometheus (`/metrics`): terkirim, gagal, latency SMTP, panjang antrian.
- Rate limit per API key / per aplikasi.

**v0.3 — Fitur email**
- Inline image (`multipart/related`, `content_id`), `List-Unsubscribe` one-click.
- Template tersimpan (`/v1/templates`) + preview.
- DKIM signing opsional untuk SMTP relay milik sendiri.
- Penjadwalan (`send_at`).

**v0.4 — Observabilitas & multi-tenant**
- Log pengiriman persisten + pencarian per penerima.
- Webhook bounce/complaint dari provider (SES/Mailgun/Postmark) dinormalisasi ke satu format.
- Dashboard admin (bisa jadi modul di `simpora2026-panel`).
