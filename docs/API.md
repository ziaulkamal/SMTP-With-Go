# Mailcore API Reference

Base URL contoh: `https://mail.internal.example.com`. Semua body adalah JSON (`Content-Type: application/json`).

## Autentikasi

Semua endpoint `/v1/*` wajib membawa salah satu header:

```
Authorization: Bearer <API_KEY>
X-API-Key: <API_KEY>
```

Setiap respons membawa `X-Request-Id` (bisa juga dikirim klien) untuk penelusuran log.

---

## Objek bersama

### `smtp`

| Field | Tipe | Wajib | Keterangan |
|-------|------|-------|------------|
| `host` | string | ✔ | Hostname/IP server SMTP. |
| `port` | int | | Default `587`. |
| `username` | string | | Kosong = tanpa AUTH. |
| `password` | string | bila `username` diisi | Tidak pernah di-log/disimpan. |
| `encryption` | `ssl` \| `starttls` \| `none` | | Default `ssl` bila port 465, selain itu `starttls`. |
| `auth` | `auto` \| `plain` \| `login` \| `none` | | Default `auto`. Office 365 → `login`. |
| `insecure_skip_verify` | bool | | Lewati verifikasi sertifikat TLS (hanya untuk self-signed internal). |
| `allow_insecure_auth` | bool | | Izinkan AUTH tanpa TLS ke host non-localhost. |

Contoh provider umum:

| Provider | host | port | encryption | auth |
|----------|------|------|------------|------|
| Gmail / Google Workspace (App Password) | `smtp.gmail.com` | 587 | `starttls` | `auto` |
| Office 365 | `smtp.office365.com` | 587 | `starttls` | `login` |
| cPanel / hosting | `mail.domain.id` | 465 | `ssl` | `auto` |
| Mailtrap (testing) | `sandbox.smtp.mailtrap.io` | 587 | `starttls` | `auto` |
| Mailpit lokal | `mailpit` / `localhost` | 1025 | `none` | `none` |

### Alamat (`from`, `to`, `cc`, `bcc`, `reply_to`)

Boleh string atau objek:

```json
"peserta@example.com"
"Budi Santoso <budi@example.com>"
{ "email": "budi@example.com", "name": "Budi Santoso" }
```

### `attachments[]`

```json
{ "filename": "jadwal.pdf", "content_type": "application/pdf", "content": "<base64>" }
```

`content_type` opsional (ditebak dari ekstensi).

### `headers`

Header tambahan, mis. `{"X-Campaign": "pendaftaran-2026", "List-Unsubscribe": "<mailto:unsub@domain.id>"}`.
Header inti (`From`, `To`, `Cc`, `Bcc`, `Reply-To`, `Subject`, `Date`, `Message-ID`, `MIME-Version`, `Content-*`, `Sender`) tidak bisa ditimpa.

---

## `POST /v1/send` — kirim satu email

Sinkron: respons dikembalikan setelah server SMTP menerima email.

```bash
curl -X POST https://mail.internal.example.com/v1/send \
  -H "Authorization: Bearer $MAILCORE_KEY" \
  -H "Content-Type: application/json" \
  -d @examples/send.json
```

```json
{
  "smtp": { "host": "smtp.gmail.com", "port": 587, "username": "noreply@simpora.id", "password": "app-password", "encryption": "starttls" },
  "from": { "email": "noreply@simpora.id", "name": "Panitia Simpora 2026" },
  "to": ["peserta@example.com"],
  "cc": [],
  "bcc": ["arsip@simpora.id"],
  "reply_to": "sekretariat@simpora.id",
  "subject": "Konfirmasi Pendaftaran",
  "text": "Terima kasih, pendaftaran Anda sudah kami terima.",
  "html": "<p>Terima kasih, pendaftaran Anda <b>sudah kami terima</b>.</p>",
  "headers": { "X-Campaign": "pendaftaran" },
  "attachments": [{ "filename": "bukti.txt", "content": "QnVrdGkgcGVuZGFmdGFyYW4=" }]
}
```

Minimal salah satu dari `text`/`html`. Total to+cc+bcc ≤ `MAILCORE_MAX_RECIPIENTS_PER_EMAIL`.

**200 OK**

```json
{
  "status": "sent",
  "message_id": "<9f1c...@simpora.id>",
  "accepted": ["peserta@example.com", "arsip@simpora.id"]
}
```

Jika sebagian penerima ditolak server: `"status": "partial"` dan `"rejected": {"x@example.com": "550 5.1.1 ..."}`.

---

## `POST /v1/send/bulk` — kirim ke banyak penerima

Asinkron. Setiap penerima mendapat **email terpisah** yang dipersonalisasi. `subject`, `text`, `html` adalah
[Go template](https://pkg.go.dev/text/template): `{{.name}}`, `{{.email}}`, dan semua key di `data`.
Key yang dipakai template **wajib** ada di `data` setiap penerima (kalau tidak → 422 sebelum apa pun dikirim).

```json
{
  "smtp": { "host": "smtp.gmail.com", "port": 587, "username": "noreply@simpora.id", "password": "app-password" },
  "from": "Panitia Simpora 2026 <noreply@simpora.id>",
  "subject": "Jadwal Pertandingan {{.cabor}} - {{.name}}",
  "text": "Halo {{.name}},\nJadwal {{.cabor}} Anda: {{.jadwal}}.",
  "html": "<p>Halo <b>{{.name}}</b>,</p><p>Jadwal {{.cabor}}: {{.jadwal}}</p>",
  "recipients": [
    { "email": "andi@example.com", "name": "Andi", "data": { "cabor": "Futsal", "jadwal": "Sabtu 08:00" } },
    { "email": "budi@example.com", "name": "Budi", "data": { "cabor": "Voli",   "jadwal": "Minggu 10:00" } }
  ],
  "options": { "concurrency": 2, "rate_per_second": 5, "max_retries": 2 },
  "callback_url": "https://core.simpora.id/api/webhooks/mailcore",
  "metadata": { "event": "jadwal-2026", "triggered_by": "admin-1" }
}
```

| `options` | Default | Keterangan |
|-----------|---------|------------|
| `concurrency` | 2 | Koneksi SMTP paralel (1..`MAILCORE_MAX_BULK_CONCURRENCY`). Gmail biasa: 1–2. |
| `rate_per_second` | 0 (tanpa batas) | Batas email/detik untuk seluruh job. Sesuaikan dengan limit provider. |
| `max_retries` | 2 | Retry untuk error sementara (4xx/jaringan). |

`metadata` dikembalikan apa adanya di status job & callback (berguna untuk mengaitkan ke data aplikasi).

**202 Accepted** (header `Location: /v1/jobs/{id}`)

```json
{
  "job_id": "job_5f0c2a...",
  "status": "queued",
  "metadata": { "event": "jadwal-2026", "triggered_by": "admin-1" },
  "created_at": "2026-10-03T08:00:00Z",
  "total": 2, "pending": 2, "sent": 0, "failed": 0, "cancelled": 0,
  "duplicates_removed": 0
}
```

---

## `GET /v1/jobs/{id}`

```json
{
  "job_id": "job_5f0c2a...",
  "status": "completed",
  "created_at": "2026-10-03T08:00:00Z",
  "started_at": "2026-10-03T08:00:00Z",
  "finished_at": "2026-10-03T08:00:02Z",
  "total": 2, "pending": 0, "sent": 1, "failed": 1, "cancelled": 0,
  "results": [
    { "email": "andi@example.com", "status": "sent", "attempts": 1, "message_id": "<...@simpora.id>", "sent_at": "2026-10-03T08:00:01Z" },
    { "email": "budi@example.com", "status": "failed", "attempts": 1, "error": "smtp rcpt_to: 550 5.1.1 mailbox unavailable" }
  ]
}
```

`?results=false` → tanpa array `results` (ringan untuk polling).

## `GET /v1/jobs`

`{"jobs": [ ...ringkasan job, terbaru dulu... ]}`

## `DELETE /v1/jobs/{id}`

Membatalkan job yang `queued`/`running`. Email yang sudah terkirim tetap terkirim; sisanya menjadi `cancelled`.
`202` bila diterima, `409 job_finished` bila job sudah selesai.

## `POST /v1/smtp/verify`

```json
{ "smtp": { "host": "smtp.gmail.com", "port": 587, "username": "noreply@simpora.id", "password": "app-password" } }
```

`200 {"ok": true, "latency_ms": 412}` atau `502` dengan detail error. Cocok untuk tombol "Tes koneksi SMTP" di panel.

## `GET /healthz`

`{"status": "ok", "version": "v0.1.0"}` — tanpa auth.

---

## Callback

Saat job selesai (status apa pun), Mailcore mengirim `POST` ke `callback_url` (maks 3 percobaan):

```
Content-Type: application/json
X-Mailcore-Event: job.finished
X-Mailcore-Signature: sha256=<hex HMAC-SHA256(body, MAILCORE_CALLBACK_SECRET)>
```

```json
{ "event": "job.finished", "job": { ...sama dengan GET /v1/jobs/{id} beserta results... } }
```

Verifikasi di Laravel:

```php
$expected = 'sha256='.hash_hmac('sha256', $request->getContent(), config('services.mailcore.callback_secret'));
abort_unless(hash_equals($expected, (string) $request->header('X-Mailcore-Signature')), 401);
```

---

## Error

Semua error berbentuk:

```json
{ "error": { "code": "smtp_auth_failed", "message": "smtp auth: 535 5.7.8 ...", "stage": "auth" } }
```

| HTTP | `code` | Kapan |
|------|--------|-------|
| 400 | `invalid_json` | JSON rusak, field tidak dikenal, body kosong. |
| 401 | `unauthorized` | API key salah/tidak ada. |
| 404 | `not_found` | Job tidak ada / sudah kedaluwarsa. |
| 409 | `job_finished` | Membatalkan job yang sudah selesai. |
| 413 | `payload_too_large` | Body melebihi `MAILCORE_MAX_BODY_BYTES`. |
| 422 | `validation_error` | Data tidak valid; `details` berisi daftar per penerima (bulk). |
| 502 | `smtp_auth_failed` | Username/password SMTP ditolak. |
| 502 | `smtp_error` | Gagal di tahap lain; lihat `stage`: `config`, `connect`, `tls`, `mail_from`, `rcpt_to`, `data`. |
| 503 | `queue_unavailable` | Antrian penuh / server shutdown (lihat `Retry-After`). |
