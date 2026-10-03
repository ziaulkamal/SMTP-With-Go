<?php

namespace App\Services;

use Illuminate\Http\Client\PendingRequest;
use Illuminate\Http\Client\RequestException;
use Illuminate\Support\Facades\Http;

/**
 * Klien sederhana untuk Mailcore API.
 *
 * config/services.php:
 *   'mailcore' => [
 *       'url' => env('MAILCORE_URL'),
 *       'key' => env('MAILCORE_KEY'),
 *       'callback_secret' => env('MAILCORE_CALLBACK_SECRET'),
 *       'smtp' => [
 *           'host' => env('MAIL_HOST'),
 *           'port' => (int) env('MAIL_PORT', 587),
 *           'username' => env('MAIL_USERNAME'),
 *           'password' => env('MAIL_PASSWORD'),
 *           'encryption' => env('MAILCORE_SMTP_ENCRYPTION', 'starttls'),
 *       ],
 *   ],
 */
class MailcoreClient
{
    public function __construct(
        private readonly string $baseUrl,
        private readonly string $apiKey,
        private readonly array $smtp,
    ) {}

    public static function fromConfig(): self
    {
        return new self(
            config('services.mailcore.url'),
            config('services.mailcore.key'),
            config('services.mailcore.smtp'),
        );
    }

    /**
     * Kirim satu email. $message berisi: from, to, cc, bcc, reply_to,
     * subject, text, html, headers, attachments.
     *
     * @throws RequestException bila SMTP/validasi gagal (lihat $e->response->json('error'))
     */
    public function send(array $message, ?array $smtp = null): array
    {
        return $this->http(60)
            ->post('/v1/send', ['smtp' => $smtp ?? $this->smtp] + $message)
            ->throw()
            ->json();
    }

    /**
     * Buat job bulk. $recipients: [['email' => ..., 'name' => ..., 'data' => [...]], ...]
     * Mengembalikan snapshot job (job_id, status, total, ...).
     */
    public function sendBulk(array $message, array $recipients, array $options = [], ?string $callbackUrl = null, array $metadata = [], ?array $smtp = null): array
    {
        return $this->http()
            ->post('/v1/send/bulk', array_filter([
                'smtp' => $smtp ?? $this->smtp,
                'recipients' => $recipients,
                'options' => $options ?: null,
                'callback_url' => $callbackUrl,
                'metadata' => $metadata ?: null,
            ]) + $message)
            ->throw()
            ->json();
    }

    public function job(string $jobId, bool $withResults = true): array
    {
        return $this->http()
            ->get("/v1/jobs/{$jobId}", ['results' => $withResults ? 'true' : 'false'])
            ->throw()
            ->json();
    }

    public function cancel(string $jobId): array
    {
        return $this->http()->delete("/v1/jobs/{$jobId}")->throw()->json();
    }

    public function verifySmtp(?array $smtp = null): bool
    {
        $response = $this->http()->post('/v1/smtp/verify', ['smtp' => $smtp ?? $this->smtp]);

        return $response->successful() && $response->json('ok') === true;
    }

    private function http(int $timeout = 15): PendingRequest
    {
        return Http::baseUrl($this->baseUrl)
            ->withToken($this->apiKey)
            ->acceptJson()
            ->asJson()
            ->timeout($timeout);
    }
}

/*
 * Contoh pemakaian:
 *
 * $mail = MailcoreClient::fromConfig();
 *
 * // Single
 * $mail->send([
 *     'from' => ['email' => 'noreply@simpora.id', 'name' => 'Panitia Simpora'],
 *     'to' => [$user->email],
 *     'subject' => 'Konfirmasi Pendaftaran',
 *     'html' => view('emails.konfirmasi', compact('user'))->render(),
 * ]);
 *
 * // Bulk
 * $job = $mail->sendBulk(
 *     [
 *         'from' => 'Panitia Simpora <noreply@simpora.id>',
 *         'subject' => 'Jadwal {{.cabor}} - {{.name}}',
 *         'html' => '<p>Halo {{.name}}, jadwal Anda: {{.jadwal}}</p>',
 *     ],
 *     $atlet->map(fn ($a) => [
 *         'email' => $a->email,
 *         'name' => $a->nama,
 *         'data' => ['cabor' => $a->cabor->nama, 'jadwal' => $a->jadwal_text],
 *     ])->all(),
 *     ['concurrency' => 2, 'rate_per_second' => 5],
 *     route('webhooks.mailcore'),
 *     ['event' => 'jadwal-2026'],
 * );
 * // simpan $job['job_id'] untuk dipantau
 */
