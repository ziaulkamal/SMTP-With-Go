# SMTP-With-Go (Mailcore)

**Mailcore** adalah layanan *mail core* berbasis **Go** yang menjadi pusat pengiriman email untuk semua aplikasi Anda.
Aplikasi lain (Laravel, Node, Python, dll) tidak perlu lagi mengurus SMTP sendiri — cukup memanggil HTTP API dengan:

1. **Credential SMTP** yang ingin dipakai (host, port, username, password, enkripsi),
2. **Pesan** yang akan dikirim (subject, isi text/HTML, lampiran, header),
3. **Penerima** — **single** (satu email) atau **bulk** (banyak penerima, masing-masing dipersonalisasi).

Satu binary, tanpa dependensi eksternal (hanya standard library Go), image Docker ±10 MB.

> 📄 Rencana & arsitektur lengkap: [`docs/PLAN.md`](docs/PLAN.md) · 📘 Referensi API detail: [`docs/API.md`](docs/API.md)

---

## Daftar Isi

1. [Fitur](#1-fitur)
2. [Persyaratan](#2-persyaratan)
3. [Instalasi & Menjalankan](#3-instalasi--menjalankan)
4. [Konfigurasi](#4-konfigurasi)
5. [Autentikasi API](#5-autentikasi-api)
6. [Panduan Penggunaan](#6-panduan-penggunaan)
   - [6.1 Cek server hidup](#61-cek-server-hidup)
   - [6.2 Tes credential SMTP](#62-tes-credential-smtp)
   - [6.3 Kirim satu email (single)](#63-kirim-satu-email-single)
   - [6.4 Kirim email dengan lampiran](#64-kirim-email-dengan-lampiran)
   - [6.5 Kirim massal (bulk) dengan template](#65-kirim-massal-bulk-dengan-template)
   - [6.6 Memantau & membatalkan job bulk](#66-memantau--membatalkan-job-bulk)
   - [6.7 Callback saat job selesai](#67-callback-saat-job-selesai)
7. [Pengaturan SMTP per Provider](#7-pengaturan-smtp-per-provider)
8. [Integrasi dengan Laravel](#8-integrasi-dengan-laravel)
9. [Kode Error & Troubleshooting](#9-kode-error--troubleshooting)
10. [Deploy ke Production](#10-deploy-ke-production)
11. [Pengembangan](#11-pengembangan)
12. [FAQ](#12-faq)

---

## 1. Fitur

| Fitur | Keterangan |
|-------|------------|
| **Single send** | `POST /v1/send` — to/cc/bcc, reply-to, text + HTML, lampiran, header custom. Respons langsung (sinkron). |
| **Bulk send** | `POST /v1/send/bulk` — ribuan penerima, 1 email per penerima, template `{{.name}}`, berjalan di background (asinkron). |
| **Template** | Subject, text, dan HTML bisa memakai variabel per penerima. Data di HTML di-escape otomatis (aman dari injeksi HTML). |
| **Andal** | Worker pool, koneksi SMTP dipakai ulang, rate limit per detik, retry otomatis untuk error sementara, berhenti otomatis bila password SMTP salah. |
| **Pemantauan** | `GET /v1/jobs/{id}` (status per penerima), `DELETE` untuk membatalkan, atau callback ber-signature HMAC. |
| **Tes SMTP** | `POST /v1/smtp/verify` — cek login SMTP tanpa mengirim email. |
| **Fleksibel** | SSL (465), STARTTLS (587), tanpa enkripsi; AUTH PLAIN / LOGIN (Office 365) / otomatis. |
| **Aman** | API key, password SMTP tidak pernah di-log/disimpan, anti header-injection, Bcc tidak bocor, login SMTP tanpa TLS ditolak. |

---

## 2. Persyaratan

Pilih salah satu:

- **Go 1.22+** (untuk build/jalan langsung), atau
- **Docker** (dan Docker Compose bila ingin sekalian Mailpit untuk testing).

Akun SMTP yang akan dipakai (Gmail App Password, Office 365, SMTP hosting/cPanel, Mailtrap, dll).

---

## 3. Instalasi & Menjalankan

### Opsi A — Langsung dengan Go

```bash
git clone https://github.com/ziaulkamal/SMTP-With-Go.git
cd SMTP-With-Go

cp .env.example .env
# edit .env → minimal isi MAILCORE_API_KEYS

set -a; source .env; set +a     # muat variabel dari .env ke shell
make run                         # atau: go run ./cmd/mailcore
```

Untuk binary siap pakai:

```bash
make build          # hasil: bin/mailcore
./bin/mailcore
```

### Opsi B — Docker

```bash
docker build -t mailcore .
docker run -d --name mailcore -p 8080:8080 --env-file .env mailcore
```

### Opsi C — Docker Compose + Mailpit (paling mudah untuk mencoba)

[Mailpit](https://mailpit.axllent.org/) adalah SMTP palsu dengan inbox web — semua email yang dikirim
ke Mailpit tidak benar-benar keluar, cocok untuk uji coba.

```bash
cp .env.example .env
docker compose --profile dev up --build
```

- API Mailcore: `http://localhost:8080`
- Inbox Mailpit: `http://localhost:8025`
- Dari Mailcore, pakai SMTP: `{"host": "mailpit", "port": 1025, "encryption": "none"}`

Server berjalan bila log menampilkan `"msg":"mailcore listening"`.

---

## 4. Konfigurasi

Semua konfigurasi lewat **environment variable** (contoh di [`.env.example`](.env.example)).

| Variabel | Default | Fungsi |
|----------|---------|--------|
| `MAILCORE_ADDR` | `:8080` | Alamat & port HTTP. |
| `MAILCORE_API_KEYS` | *(kosong)* | Daftar API key, pisahkan koma. **Wajib di production.** Disarankan 1 key per aplikasi. |
| `MAILCORE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `MAILCORE_MAX_BODY_BYTES` | `26214400` (25 MB) | Ukuran maksimal request (termasuk lampiran base64). |
| `MAILCORE_MAX_RECIPIENTS_PER_EMAIL` | `50` | Batas to+cc+bcc di `/v1/send`. |
| `MAILCORE_MAX_BULK_RECIPIENTS` | `5000` | Batas penerima per request bulk. |
| `MAILCORE_MAX_BULK_CONCURRENCY` | `10` | Batas `options.concurrency`. |
| `MAILCORE_MAX_RETRIES` | `5` | Batas `options.max_retries`. |
| `MAILCORE_JOB_WORKERS` | `4` | Jumlah job bulk yang diproses bersamaan. |
| `MAILCORE_JOB_QUEUE_SIZE` | `100` | Kapasitas antrian job. |
| `MAILCORE_JOB_TTL` | `24h` | Lama hasil job bisa dilihat setelah selesai. |
| `MAILCORE_SMTP_TIMEOUT` | `30s` | Timeout koneksi/login SMTP dan per email. |
| `MAILCORE_HELO_NAME` | hostname | Nama yang diumumkan di perintah `EHLO`. |
| `MAILCORE_CALLBACK_SECRET` | *(kosong)* | Kunci HMAC untuk tanda tangan callback. |

Membuat API key acak:

```bash
openssl rand -hex 32
```

> ⚠️ Bila `MAILCORE_API_KEYS` kosong, API **terbuka untuk siapa saja** (server akan menampilkan peringatan). Hanya untuk development lokal.

---

## 5. Autentikasi API

Semua endpoint `/v1/*` wajib mengirim API key, pilih salah satu header:

```
Authorization: Bearer <API_KEY>
X-API-Key: <API_KEY>
```

Untuk contoh di bawah, siapkan variabel:

```bash
export MC=http://localhost:8080
export KEY=api-key-anda
```

---

## 6. Panduan Penggunaan

### Format alamat email

Di semua field alamat (`from`, `to`, `cc`, `bcc`, `reply_to`, `recipients`) Anda boleh memakai bentuk:

```json
"budi@example.com"
"Budi Santoso <budi@example.com>"
{ "email": "budi@example.com", "name": "Budi Santoso" }
```

### Blok `smtp`

Setiap request membawa credential SMTP yang ingin dipakai:

```json
"smtp": {
  "host": "smtp.gmail.com",
  "port": 587,
  "username": "noreply@domainanda.id",
  "password": "app-password",
  "encryption": "starttls",
  "auth": "auto"
}
```

| Field | Wajib | Nilai |
|-------|-------|-------|
| `host` | ✔ | Hostname/IP server SMTP. |
| `port` | | Default `587`. |
| `username`, `password` | | Kosongkan `username` bila server tidak butuh login. |
| `encryption` | | `starttls` (default), `ssl` (otomatis bila port 465), `none`. |
| `auth` | | `auto` (default), `plain`, `login`, `none`. |
| `insecure_skip_verify` | | `true` untuk sertifikat self-signed (jaringan internal saja). |
| `allow_insecure_auth` | | `true` untuk mengizinkan login tanpa TLS ke host selain localhost (tidak disarankan). |

---

### 6.1 Cek server hidup

```bash
curl $MC/healthz
# {"status":"ok","version":"dev"}
```

### 6.2 Tes credential SMTP

Sebelum mengirim, pastikan host/port/password benar. Tidak ada email yang dikirim.

```bash
curl -X POST $MC/v1/smtp/verify \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "smtp": {"host": "smtp.gmail.com", "port": 587, "username": "noreply@domainanda.id", "password": "app-password"}
  }'
```

Berhasil → `{"ok":true,"latency_ms":412}`. Gagal → HTTP `502` dengan penjelasan, mis. `"code":"smtp_auth_failed"`.

### 6.3 Kirim satu email (single)

```bash
curl -X POST $MC/v1/send \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "smtp": {"host": "smtp.gmail.com", "port": 587, "username": "noreply@domainanda.id", "password": "app-password"},
    "from": {"email": "noreply@domainanda.id", "name": "Panitia Simpora 2026"},
    "to": ["peserta@example.com"],
    "cc": ["koordinator@example.com"],
    "bcc": ["arsip@domainanda.id"],
    "reply_to": "sekretariat@domainanda.id",
    "subject": "Konfirmasi Pendaftaran",
    "text": "Terima kasih, pendaftaran Anda sudah kami terima.",
    "html": "<p>Terima kasih, pendaftaran Anda <b>sudah kami terima</b>.</p>",
    "headers": {"X-Campaign": "pendaftaran"}
  }'
```

Respons:

```json
{
  "status": "sent",
  "message_id": "<9f1c0a...@domainanda.id>",
  "accepted": ["peserta@example.com", "koordinator@example.com", "arsip@domainanda.id"]
}
```

Catatan:
- Wajib ada `subject` dan minimal salah satu `text` / `html`. Mengisi keduanya disarankan (klien email lama menampilkan versi text).
- Bila sebagian penerima ditolak server, `status` menjadi `"partial"` dan alamat yang ditolak ada di `rejected`.
- Penerima `bcc` tidak terlihat oleh penerima lain.
- Untuk lebih dari `MAILCORE_MAX_RECIPIENTS_PER_EMAIL` penerima, gunakan **bulk**.

### 6.4 Kirim email dengan lampiran

Isi lampiran dikirim sebagai **base64**:

```bash
# Linux
base64 -w0 jadwal.pdf > jadwal.b64
# macOS
base64 -i jadwal.pdf -o jadwal.b64
```

```json
"attachments": [
  { "filename": "jadwal.pdf", "content_type": "application/pdf", "content": "JVBERi0xLjQKJ..." },
  { "filename": "catatan.txt", "content": "SGFsbyBkdW5pYQ==" }
]
```

`content_type` boleh dikosongkan — akan ditebak dari ekstensi file. Total ukuran request maksimal `MAILCORE_MAX_BODY_BYTES`
(base64 ±33% lebih besar dari file aslinya).

### 6.5 Kirim massal (bulk) dengan template

Setiap penerima mendapat **email sendiri** (penerima lain tidak terlihat). `subject`, `text`, dan `html` memakai
sintaks template Go: `{{.name}}`, `{{.email}}`, dan semua key di dalam `data`.

```bash
curl -X POST $MC/v1/send/bulk \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "smtp": {"host": "smtp.gmail.com", "port": 587, "username": "noreply@domainanda.id", "password": "app-password"},
    "from": "Panitia Simpora 2026 <noreply@domainanda.id>",
    "subject": "Jadwal Pertandingan {{.cabor}} - {{.name}}",
    "text": "Halo {{.name}},\nJadwal {{.cabor}} Anda: {{.jadwal}}.",
    "html": "<p>Halo <b>{{.name}}</b>,</p><p>Jadwal {{.cabor}}: {{.jadwal}}</p>",
    "recipients": [
      {"email": "andi@example.com", "name": "Andi", "data": {"cabor": "Futsal", "jadwal": "Sabtu 08:00"}},
      {"email": "budi@example.com", "name": "Budi", "data": {"cabor": "Voli",   "jadwal": "Minggu 10:00"}}
    ],
    "options": {"concurrency": 2, "rate_per_second": 5, "max_retries": 2},
    "callback_url": "https://app-anda.id/api/webhooks/mailcore",
    "metadata": {"event": "jadwal-2026"}
  }'
```

Respons `202 Accepted`:

```json
{ "job_id": "job_5f0c2a...", "status": "queued", "total": 2, "pending": 2, "sent": 0, "failed": 0, "cancelled": 0 }
```

**Opsi pengiriman (`options`)**

| Opsi | Default | Penjelasan |
|------|---------|------------|
| `concurrency` | `2` | Jumlah koneksi SMTP paralel. Gmail/akun biasa: 1–2. Server sendiri: bisa lebih. |
| `rate_per_second` | `0` (tanpa batas) | Maksimal email per detik untuk seluruh job. **Sesuaikan dengan limit provider** agar tidak diblokir. |
| `max_retries` | `2` | Berapa kali mencoba ulang bila error sementara (server sibuk, jaringan putus). |

**Aturan penting bulk**
- Semua penerima **divalidasi dulu**. Jika ada alamat salah atau variabel template yang tidak ada di `data`,
  request ditolak `422` beserta daftar kesalahannya, dan **tidak ada satu pun email yang dikirim**.
- Alamat duplikat dibuang otomatis (jumlahnya di `duplicates_removed`).
- `recipients` juga boleh berupa string biasa: `["a@x.com", "Budi <b@x.com>"]` (bila template hanya memakai `name`/`email`).
- `metadata` dikembalikan apa adanya di status job & callback — gunakan untuk menautkan ke data di aplikasi Anda.

### 6.6 Memantau & membatalkan job bulk

```bash
# status lengkap + hasil per penerima
curl $MC/v1/jobs/job_5f0c2a... -H "Authorization: Bearer $KEY"

# ringkasan saja (lebih ringan untuk polling)
curl "$MC/v1/jobs/job_5f0c2a...?results=false" -H "Authorization: Bearer $KEY"

# daftar semua job
curl $MC/v1/jobs -H "Authorization: Bearer $KEY"

# batalkan job
curl -X DELETE $MC/v1/jobs/job_5f0c2a... -H "Authorization: Bearer $KEY"
```

Contoh hasil:

```json
{
  "job_id": "job_5f0c2a...",
  "status": "completed",
  "total": 2, "pending": 0, "sent": 1, "failed": 1, "cancelled": 0,
  "results": [
    {"email": "andi@example.com", "status": "sent", "attempts": 1, "message_id": "<...>", "sent_at": "2026-10-03T08:00:01Z"},
    {"email": "budi@example.com", "status": "failed", "attempts": 1, "error": "smtp rcpt_to: 550 5.1.1 mailbox unavailable"}
  ]
}
```

| Status job | Arti |
|------------|------|
| `queued` | Menunggu giliran. |
| `running` | Sedang mengirim. |
| `completed` | Semua penerima sudah diproses — cek angka `sent` dan `failed`. |
| `failed` | Job dihentikan karena error fatal (biasanya username/password SMTP salah). |
| `cancelled` | Dibatalkan lewat `DELETE` atau server dimatikan. |

Status per penerima: `pending`, `sent`, `failed`, `cancelled`.

> Hasil job disimpan di memori selama `MAILCORE_JOB_TTL` (default 24 jam). Simpan hasil yang penting di database aplikasi Anda
> (paling mudah lewat callback).

### 6.7 Callback saat job selesai

Bila `callback_url` diisi, Mailcore mengirim `POST` ke URL tersebut ketika job selesai (maks. 3 percobaan):

```
X-Mailcore-Event: job.finished
X-Mailcore-Signature: sha256=<HMAC-SHA256 dari body, kunci MAILCORE_CALLBACK_SECRET>
```

```json
{ "event": "job.finished", "job": { "job_id": "...", "status": "completed", "sent": 2, "results": [ ... ] } }
```

Verifikasi signature di Laravel:

```php
Route::post('/api/webhooks/mailcore', function (Illuminate\Http\Request $request) {
    $expected = 'sha256='.hash_hmac('sha256', $request->getContent(), config('services.mailcore.callback_secret'));
    abort_unless(hash_equals($expected, (string) $request->header('X-Mailcore-Signature')), 401);

    $job = $request->input('job');
    // simpan $job['results'] ke database, update status kampanye, dll.
    return response()->noContent();
});
```

---

## 7. Pengaturan SMTP per Provider

| Provider | `host` | `port` | `encryption` | `auth` | Catatan |
|----------|--------|--------|--------------|--------|---------|
| Gmail / Google Workspace | `smtp.gmail.com` | 587 | `starttls` | `auto` | Wajib **App Password** (aktifkan 2FA → Akun Google → Keamanan → *App passwords*). Limit ±500 email/hari (akun biasa). |
| Office 365 / Outlook | `smtp.office365.com` | 587 | `starttls` | `login` | SMTP AUTH harus diaktifkan untuk mailbox di admin center. |
| Hosting / cPanel | `mail.domainanda.id` | 465 | `ssl` | `auto` | Atau port 587 + `starttls`. |
| Zoho Mail | `smtp.zoho.com` | 465 | `ssl` | `auto` | |
| Amazon SES (SMTP) | `email-smtp.<region>.amazonaws.com` | 587 | `starttls` | `auto` | Pakai SMTP credentials SES, bukan access key IAM. |
| Mailtrap (testing) | `sandbox.smtp.mailtrap.io` | 587 | `starttls` | `auto` | |
| Mailpit lokal | `mailpit` / `localhost` | 1025 | `none` | `none` | Tanpa username/password. |

Tips agar email tidak masuk spam: gunakan alamat `from` dari domain yang sama dengan akun SMTP, dan pastikan domain
memiliki record **SPF**, **DKIM**, dan **DMARC** yang benar.

---

## 8. Integrasi dengan Laravel

Salin [`examples/laravel/MailcoreClient.php`](examples/laravel/MailcoreClient.php) ke `app/Services/MailcoreClient.php`.

`.env` aplikasi Laravel:

```env
MAILCORE_URL=http://mailcore:8080
MAILCORE_KEY=api-key-untuk-aplikasi-ini
MAILCORE_CALLBACK_SECRET=sama-dengan-di-server-mailcore
MAIL_HOST=smtp.gmail.com
MAIL_PORT=587
MAIL_USERNAME=noreply@domainanda.id
MAIL_PASSWORD=app-password
MAILCORE_SMTP_ENCRYPTION=starttls
```

`config/services.php`:

```php
'mailcore' => [
    'url' => env('MAILCORE_URL'),
    'key' => env('MAILCORE_KEY'),
    'callback_secret' => env('MAILCORE_CALLBACK_SECRET'),
    'smtp' => [
        'host' => env('MAIL_HOST'),
        'port' => (int) env('MAIL_PORT', 587),
        'username' => env('MAIL_USERNAME'),
        'password' => env('MAIL_PASSWORD'),
        'encryption' => env('MAILCORE_SMTP_ENCRYPTION', 'starttls'),
    ],
],
```

Pemakaian:

```php
use App\Services\MailcoreClient;

$mail = MailcoreClient::fromConfig();

// Single
$mail->send([
    'from' => ['email' => 'noreply@domainanda.id', 'name' => 'Panitia Simpora'],
    'to' => [$user->email],
    'subject' => 'Konfirmasi Pendaftaran',
    'html' => view('emails.konfirmasi', compact('user'))->render(),
]);

// Bulk
$job = $mail->sendBulk(
    [
        'from' => 'Panitia Simpora <noreply@domainanda.id>',
        'subject' => 'Jadwal {{.cabor}} - {{.name}}',
        'html' => '<p>Halo {{.name}}, jadwal Anda: {{.jadwal}}</p>',
    ],
    $atlet->map(fn ($a) => [
        'email' => $a->email,
        'name' => $a->nama,
        'data' => ['cabor' => $a->cabor->nama, 'jadwal' => $a->jadwal_text],
    ])->all(),
    ['concurrency' => 2, 'rate_per_second' => 5],
    route('webhooks.mailcore'),
    ['event' => 'jadwal-2026'],
);

// simpan $job['job_id'], lalu pantau:
$status = $mail->job($job['job_id'], withResults: false);
```

Anda juga bisa memakai credential SMTP berbeda per pemanggilan (misal tiap event punya akun sendiri) dengan
mengirim parameter `$smtp`.

> 💡 Template bulk memakai sintaks `{{.nama}}` milik Go. Di file Blade, tulis sebagai `@{{.nama}}` agar tidak diproses Blade.

---

## 9. Kode Error & Troubleshooting

Format error selalu:

```json
{ "error": { "code": "smtp_auth_failed", "message": "smtp auth: 535 5.7.8 Username and Password not accepted", "stage": "auth" } }
```

| HTTP | `code` | Penyebab | Solusi |
|------|--------|----------|--------|
| 400 | `invalid_json` | JSON rusak, body kosong, atau **nama field salah ketik** | Periksa nama field sesuai dokumentasi. |
| 401 | `unauthorized` | API key salah/tidak dikirim | Periksa header `Authorization`/`X-API-Key`. |
| 404 | `not_found` | Job tidak ada atau sudah lewat TTL | — |
| 409 | `job_finished` | Membatalkan job yang sudah selesai | — |
| 413 | `payload_too_large` | Request (lampiran) terlalu besar | Perkecil lampiran atau naikkan `MAILCORE_MAX_BODY_BYTES`. |
| 422 | `validation_error` | Alamat tidak valid, subject kosong, variabel template tidak ada, dll | Lihat `message` dan `details`. |
| 502 | `smtp_auth_failed` | Username/password SMTP ditolak | Gmail: pakai App Password. O365: `auth: "login"` + aktifkan SMTP AUTH. |
| 502 | `smtp_error` | Lihat `stage` | lihat tabel di bawah |
| 503 | `queue_unavailable` | Antrian bulk penuh | Coba lagi setelah `Retry-After`, atau naikkan `MAILCORE_JOB_QUEUE_SIZE`. |

| `stage` | Arti | Yang perlu dicek |
|---------|------|------------------|
| `config` | Konfigurasi SMTP tidak valid | Nilai `encryption`, `auth`, `port`. |
| `connect` | Tidak bisa terhubung | Host/port, firewall, apakah server/VPS memblokir port 25/465/587. |
| `tls` | Gagal TLS / STARTTLS tidak didukung | Port 465 → `ssl`; port 587 → `starttls`; sertifikat self-signed → `insecure_skip_verify`. |
| `auth` | Login ditolak | Username/password, App Password, metode `auth`. |
| `mail_from` | Pengirim ditolak | Alamat `from` harus milik/diizinkan akun SMTP. |
| `rcpt_to` | Penerima ditolak | Alamat tujuan tidak ada atau diblokir. |
| `data` | Isi email ditolak | Ukuran terlalu besar, terdeteksi spam, atau limit harian tercapai. |

Pesan *"refusing to send credentials over an unencrypted connection"* berarti Anda memakai `encryption: "none"`
ke server di luar localhost. Gunakan `starttls`/`ssl`, atau (hanya di jaringan tepercaya) set `allow_insecure_auth: true`.

---

## 10. Deploy ke Production

1. **Isi `MAILCORE_API_KEYS`** dengan key acak yang panjang (satu per aplikasi agar mudah dicabut).
2. **Jalankan di belakang HTTPS** (Nginx/Caddy/Traefik) atau hanya di jaringan privat, karena request membawa password SMTP.
3. Isi `MAILCORE_CALLBACK_SECRET` bila memakai callback.
4. Pastikan server mengizinkan koneksi keluar ke port SMTP (587/465). Banyak VPS memblokir port 25.

Contoh Nginx:

```nginx
server {
    listen 443 ssl;
    server_name mail-api.domainanda.id;
    # ssl_certificate ...; ssl_certificate_key ...;

    client_max_body_size 30m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Request-Id $request_id;
        proxy_read_timeout 120s;
    }
}
```

Contoh service systemd (`/etc/systemd/system/mailcore.service`):

```ini
[Unit]
Description=Mailcore API
After=network-online.target

[Service]
EnvironmentFile=/etc/mailcore.env
ExecStart=/usr/local/bin/mailcore
Restart=always
User=mailcore
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now mailcore
journalctl -u mailcore -f       # log dalam format JSON
```

Saat dimatikan (`SIGTERM`), server berhenti dengan rapi: request berjalan diselesaikan, job bulk yang belum selesai
ditandai `cancelled`.

---

## 11. Pengembangan

```bash
make test     # go test -race ./...
make lint     # gofmt + go vet
make build    # bin/mailcore
make docker   # image docker mailcore:<versi>
```

Struktur proyek:

```
cmd/mailcore/        entrypoint (HTTP server, graceful shutdown)
internal/api/        handler HTTP, auth API key, validasi, format error
internal/mailer/     alamat, MIME builder, SMTP client, template
internal/job/        job bulk: antrian, worker pool, retry, rate limit, callback
internal/config/     konfigurasi dari environment
internal/smtptest/   SMTP server palsu untuk test
docs/                PLAN.md (rencana prototype), API.md (referensi API)
examples/            contoh JSON request + klien Laravel
```

Test berjalan sepenuhnya lokal memakai SMTP server palsu (`internal/smtptest`), tidak butuh koneksi internet.

---

## 12. FAQ

**Apakah Mailcore menyimpan password SMTP saya?**
Tidak. Password hanya ada di memori selama request/job berjalan, tidak pernah ditulis ke log atau disk, dan dihapus setelah job selesai.

**Apa yang terjadi jika server restart saat bulk berjalan?**
Pada versi prototype antrian ada di memori: job yang belum selesai ditandai `cancelled`. Penerima yang sudah `sent` tetap terkirim.
Kirim ulang hanya penerima yang belum terkirim (lihat `results`). Antrian persisten ada di roadmap ([`docs/PLAN.md`](docs/PLAN.md)).

**Bisakah satu Mailcore dipakai banyak aplikasi dengan akun SMTP berbeda?**
Bisa — itu tujuan utamanya. Setiap request membawa credential SMTP-nya sendiri; beri setiap aplikasi API key berbeda.

**Kenapa bulk mengirim satu email per penerima, bukan satu email ke banyak `to`?**
Agar alamat penerima tidak saling terlihat, isi bisa dipersonalisasi, dan kegagalan satu alamat tidak menggagalkan yang lain.

**Berapa `rate_per_second` yang aman?**
Tergantung provider. Gmail akun biasa: ±1 email/detik dan ±500/hari. Server SMTP sendiri atau layanan seperti SES: bisa jauh lebih tinggi sesuai kuota.
