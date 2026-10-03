# Mailcore

Mail core API berbasis **Go** untuk semua aplikasi Anda. Aplikasi lain cukup memanggil HTTP API dengan:

1. **credential SMTP** yang ingin dipakai (host, port, username, password, enkripsi),
2. **pesan** yang akan dikirim (subject, text/html, lampiran, header),
3. **penerima** — satu email (**single**) atau banyak penerima yang dipersonalisasi (**bulk**).

Tanpa dependensi eksternal (hanya standard library Go), satu binary, image Docker kecil.

📄 **Prototype plan lengkap:** [`docs/PLAN.md`](docs/PLAN.md) · 📘 **Referensi API:** [`docs/API.md`](docs/API.md)

## Fitur

- `POST /v1/send` — kirim satu email (to/cc/bcc, reply-to, text + html, lampiran, header custom), sinkron.
- `POST /v1/send/bulk` — kirim ke ribuan penerima sebagai job asinkron, 1 email per penerima dengan template
  `{{.name}}`, `{{.data_apa_saja}}`.
  - worker pool + reuse koneksi SMTP, rate limit per detik, retry otomatis untuk error sementara,
  - validasi semua penerima sebelum dikirim, buang duplikat,
  - pantau via `GET /v1/jobs/{id}`, batalkan via `DELETE`, atau terima **callback** ber-signature HMAC.
- `POST /v1/smtp/verify` — tes login SMTP tanpa mengirim email.
- SMTP: `ssl` (465), `starttls` (587), `none`; AUTH `PLAIN` / `LOGIN` (Office 365) / otomatis.
- Keamanan: API key, password SMTP tidak pernah di-log/disimpan, anti header-injection, Bcc tidak bocor,
  AUTH ditolak tanpa TLS (kecuali localhost/opt-in), escape HTML otomatis pada template.

## Mulai Cepat

```bash
cp .env.example .env            # isi MAILCORE_API_KEYS
make run                        # atau: go run ./cmd/mailcore
```

Dengan Docker + Mailpit (inbox testing di http://localhost:8025):

```bash
docker compose --profile dev up --build
```

Kirim email percobaan (pakai Mailpit sebagai SMTP):

```bash
export KEY=ganti-dengan-key-acak-panjang

# ganti "localhost" di examples/*.json menjadi "mailpit" bila mailcore berjalan di docker compose
curl -s localhost:8080/v1/send -H "Authorization: Bearer $KEY" -d @examples/send.json

JOB=$(curl -s localhost:8080/v1/send/bulk -H "Authorization: Bearer $KEY" -d @examples/bulk.json | jq -r .job_id)
curl -s localhost:8080/v1/jobs/$JOB -H "Authorization: Bearer $KEY" | jq
```

## Contoh Request

**Single**

```json
POST /v1/send
{
  "smtp": { "host": "smtp.gmail.com", "port": 587, "username": "noreply@simpora.id", "password": "app-password", "encryption": "starttls" },
  "from": { "email": "noreply@simpora.id", "name": "Panitia Simpora" },
  "to": ["peserta@example.com"],
  "subject": "Konfirmasi Pendaftaran",
  "html": "<p>Pendaftaran Anda sudah kami terima.</p>"
}
→ 200 {"status":"sent","message_id":"<...@simpora.id>","accepted":["peserta@example.com"]}
```

**Bulk**

```json
POST /v1/send/bulk
{
  "smtp": { ... },
  "from": "Panitia Simpora <noreply@simpora.id>",
  "subject": "Jadwal {{.cabor}} - {{.name}}",
  "html": "<p>Halo {{.name}}, jadwal Anda: {{.jadwal}}</p>",
  "recipients": [
    { "email": "andi@example.com", "name": "Andi", "data": { "cabor": "Futsal", "jadwal": "Sabtu 08:00" } },
    { "email": "budi@example.com", "name": "Budi", "data": { "cabor": "Voli", "jadwal": "Minggu 10:00" } }
  ],
  "options": { "concurrency": 2, "rate_per_second": 5, "max_retries": 2 },
  "callback_url": "https://core.simpora.id/api/webhooks/mailcore"
}
→ 202 {"job_id":"job_...","status":"queued","total":2,...}
```

Klien Laravel siap pakai: [`examples/laravel/MailcoreClient.php`](examples/laravel/MailcoreClient.php).

## Struktur Proyek

```
cmd/mailcore/        entrypoint (HTTP server, graceful shutdown)
internal/api/        handler HTTP, auth, validasi, error
internal/mailer/     alamat, MIME builder, SMTP client, template
internal/job/        job bulk: antrian, worker pool, retry, rate limit, callback
internal/config/     konfigurasi dari env
internal/smtptest/   SMTP server palsu untuk test
docs/                PLAN.md (prototype plan), API.md
examples/            contoh JSON + klien Laravel
```

## Pengembangan

```bash
make test    # go test -race ./...
make lint    # gofmt + go vet
make build   # bin/mailcore
make docker  # image mailcore:<versi>
```

> ⚠️ Request membawa password SMTP — selalu jalankan Mailcore di jaringan privat dan/atau di belakang HTTPS,
> dan isi `MAILCORE_API_KEYS` di production.
