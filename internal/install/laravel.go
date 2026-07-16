package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/status"
)

type LaravelHookInstaller struct {
	Now func() time.Time
}

func NewLaravelHookInstaller() *LaravelHookInstaller {
	return &LaravelHookInstaller{Now: time.Now}
}

type LocawebBootstrapInstaller struct{}

func NewLocawebBootstrapInstaller() *LocawebBootstrapInstaller {
	return &LocawebBootstrapInstaller{}
}

type LocawebConfigInitializer struct{}

func NewLocawebConfigInitializer() *LocawebConfigInitializer {
	return &LocawebConfigInitializer{}
}

func (i *LaravelHookInstaller) Install(projectRoot string, force bool) ([]string, error) {
	if err := i.validateProject(projectRoot); err != nil {
		return nil, err
	}

	timestamp := i.Now().UTC().Format("2006_01_02_150405")
	migrationFile := filepath.Join(projectRoot, "database", "migrations", timestamp+"_create_deploy_hook_executions_table.php")

	files := map[string]string{
		filepath.Join(projectRoot, "config", "deploypier.php"):                                              configTemplate,
		filepath.Join(projectRoot, "app", "Http", "Controllers", "Internal", "DeployReceiveController.php"): controllerTemplate,
		filepath.Join(projectRoot, "app", "Http", "Middleware", "EnsureValidDeployHookSignature.php"):       middlewareTemplate,
		filepath.Join(projectRoot, "app", "Http", "Requests", "Internal", "ReceiveDeployHookRequest.php"):   requestTemplate,
		filepath.Join(projectRoot, "app", "Models", "DeployHookExecution.php"):                              modelTemplate,
		filepath.Join(projectRoot, "app", "Services", "Deploy", "DeployHookSignatureService.php"):           signatureServiceTemplate,
		filepath.Join(projectRoot, "app", "Services", "Deploy", "DeployHookPipelineService.php"):            pipelineServiceTemplate,
		filepath.Join(projectRoot, "app", "Services", "Deploy", "DeployHookReceiverService.php"):            receiverServiceTemplate,
		migrationFile: migrationTemplate,
		filepath.Join(projectRoot, "tests", "Feature", "Infrastructure", "DeployHookReceiverTest.php"): featureTestTemplate,
		filepath.Join(projectRoot, "tests", "Unit", "Deploy", "DeployHookSignatureServiceTest.php"):    unitTestTemplate,
	}

	created := make([]string, 0, len(files)+2)
	for path, content := range files {
		if err := writeFile(path, content, force); err != nil {
			return created, err
		}
		created = append(created, path)
	}

	routePath := filepath.Join(projectRoot, "routes", "api.php")
	if changed, err := ensureFile(routePath, apiRoutesTemplate); err != nil {
		return created, err
	} else if changed {
		created = append(created, routePath)
	}
	if changed, err := ensureContains(routePath, routeSnippet); err != nil {
		return created, err
	} else if changed {
		created = append(created, routePath)
	}

	bootstrapPath := filepath.Join(projectRoot, "bootstrap", "app.php")
	if changed, err := ensureLaravelAPIRouting(bootstrapPath); err != nil {
		return created, err
	} else if changed {
		created = append(created, bootstrapPath)
	}

	envPath := filepath.Join(projectRoot, ".env.example")
	if _, err := os.Stat(envPath); err == nil {
		if changed, err := ensureContains(envPath, envSnippet); err != nil {
			return created, err
		} else if changed {
			created = append(created, envPath)
		}
	}

	return created, nil
}

func (i *LaravelHookInstaller) validateProject(projectRoot string) error {
	required := []string{
		filepath.Join(projectRoot, "artisan"),
		filepath.Join(projectRoot, "composer.json"),
		filepath.Join(projectRoot, "bootstrap", "app.php"),
	}

	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return status.Wrap(status.KindConfig, "validate laravel project", fmt.Errorf("required path missing: %s", path))
		}
	}

	return nil
}

func (i *LocawebBootstrapInstaller) Install(projectRoot string, ftpUser string, force bool) ([]string, error) {
	if strings.TrimSpace(ftpUser) == "" {
		return nil, status.Wrap(status.KindConfig, "validate locaweb bootstrap", fmt.Errorf("ftp user is required"))
	}

	validator := NewLaravelHookInstaller()
	if err := validator.validateProject(projectRoot); err != nil {
		return nil, err
	}

	files := map[string]string{
		filepath.Join(projectRoot, "scripts", "locaweb", "bootstrap-first-deploy.sh"): renderLocawebBootstrapScript(ftpUser),
		filepath.Join(projectRoot, "scripts", "locaweb", "fix-storage-link.sh"):       locawebStorageLinkScript,
		filepath.Join(projectRoot, "docs", "locaweb-bootstrap.md"):                    renderLocawebBootstrapDoc(ftpUser),
	}

	created := make([]string, 0, len(files))
	for path, content := range files {
		if err := writeFile(path, content, force); err != nil {
			return created, err
		}
		created = append(created, path)
	}

	return created, nil
}

func (i *LocawebConfigInitializer) Install(projectRoot string, ftpUser string, force bool) ([]string, error) {
	if strings.TrimSpace(ftpUser) == "" {
		return nil, status.Wrap(status.KindConfig, "init locaweb config", fmt.Errorf("ftp user is required"))
	}

	validator := NewLaravelHookInstaller()
	if err := validator.validateProject(projectRoot); err != nil {
		return nil, err
	}

	projectName := filepath.Base(projectRoot)
	files := map[string]string{
		filepath.Join(projectRoot, "deploy.yml"):                                  renderLocawebDeployYAML(projectName, ftpUser),
		filepath.Join(projectRoot, ".deploy.env.example"):                         renderLocawebDeployEnvExample(ftpUser),
		filepath.Join(projectRoot, "docs", "deploy-locaweb.md"):                   renderLocawebDeployGuide(projectName, ftpUser),
		filepath.Join(projectRoot, "docs", "deploypier-public-index.php.example"): renderLocawebPublicIndexExample(ftpUser),
	}

	created := make([]string, 0, len(files))
	for path, content := range files {
		if err := writeFile(path, content, force); err != nil {
			return created, err
		}
		created = append(created, path)
	}

	return created, nil
}

func writeFile(path string, content string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return status.Wrap(status.KindConflict, "write scaffold file", fmt.Errorf("file already exists: %s", path))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return status.Wrap(status.KindInternal, "mkdir scaffold path", err)
	}

	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		return status.Wrap(status.KindInternal, "write scaffold file", err)
	}

	return nil
}

func ensureFile(path string, content string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, status.Wrap(status.KindInternal, "stat scaffold file", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, status.Wrap(status.KindInternal, "mkdir scaffold path", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		return false, status.Wrap(status.KindInternal, "write scaffold file", err)
	}
	return true, nil
}

func ensureContains(path string, snippet string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, status.Wrap(status.KindInternal, "read file for append", err)
	}

	content := string(raw)
	trimmedSnippet := strings.TrimLeft(snippet, "\n")
	if strings.Contains(content, trimmedSnippet) {
		return false, nil
	}

	var builder strings.Builder
	builder.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(trimmedSnippet)

	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return false, status.Wrap(status.KindInternal, "append scaffold snippet", err)
	}

	return true, nil
}

func ensureLaravelAPIRouting(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, status.Wrap(status.KindInternal, "read bootstrap app", err)
	}

	content := string(raw)
	if strings.Contains(content, "routes/api.php") || strings.Contains(content, "api: __DIR__.'/../routes/api.php'") {
		return false, nil
	}

	replacement := "\n        api: __DIR__.'/../routes/api.php',"
	anchors := []string{
		"web: __DIR__.'/../routes/web.php',",
		"commands: __DIR__.'/../routes/console.php',",
		"health: '/up',",
		"->withRouting(",
	}

	updated := content
	changed := false
	for _, anchor := range anchors {
		if !strings.Contains(updated, anchor) {
			continue
		}
		if anchor == "->withRouting(" {
			updated = strings.Replace(updated, anchor, anchor+replacement, 1)
		} else {
			updated = strings.Replace(updated, anchor, anchor+replacement, 1)
		}
		changed = true
		break
	}

	if !changed {
		return false, status.Wrap(status.KindConflict, "update bootstrap app", fmt.Errorf("could not find a withRouting anchor inside %s", path))
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, status.Wrap(status.KindInternal, "write bootstrap app", err)
	}
	return true, nil
}

const routeSnippet = `
Route::post('/internal/deploy/receive', \App\Http\Controllers\Internal\DeployReceiveController::class)
    ->middleware([\App\Http\Middleware\EnsureValidDeployHookSignature::class, 'throttle:20,1'])
    ->name('internal.deploy.receive');
`

const apiRoutesTemplate = `
<?php

use Illuminate\Support\Facades\Route;
`

const envSnippet = `
SYSTEM_DEPLOY_RECEIVER_ENABLED=false
SYSTEM_DEPLOY_RECEIVER_KEY_ID=
SYSTEM_DEPLOY_RECEIVER_SECRET=
SYSTEM_DEPLOY_RECEIVER_SIGNATURE_VERSION=v1
SYSTEM_DEPLOY_RECEIVER_SIGNATURE_SCOPE=deploy:post-deploy
SYSTEM_DEPLOY_RECEIVER_REPLAY_TOLERANCE_SECONDS=300
SYSTEM_DEPLOY_RECEIVER_REQUEST_TIMEOUT_SECONDS=900
SYSTEM_DEPLOY_RECEIVER_LOCK_SECONDS=900
SYSTEM_DEPLOY_RECEIVER_LOCK_KEY=deploypier:receiver
SYSTEM_DEPLOY_RECEIVER_QUEUE_RESTART=true
`

const configTemplate = `
<?php

return [
    'enabled' => filter_var(env('SYSTEM_DEPLOY_RECEIVER_ENABLED', false), FILTER_VALIDATE_BOOL),
    'key_id' => env('SYSTEM_DEPLOY_RECEIVER_KEY_ID'),
    'secret' => env('SYSTEM_DEPLOY_RECEIVER_SECRET'),
    'signature_version' => env('SYSTEM_DEPLOY_RECEIVER_SIGNATURE_VERSION', 'v1'),
    'signature_scope' => env('SYSTEM_DEPLOY_RECEIVER_SIGNATURE_SCOPE', 'deploy:post-deploy'),
    'replay_tolerance_seconds' => max(5, (int) env('SYSTEM_DEPLOY_RECEIVER_REPLAY_TOLERANCE_SECONDS', 300)),
    'request_timeout_seconds' => max(30, (int) env('SYSTEM_DEPLOY_RECEIVER_REQUEST_TIMEOUT_SECONDS', 900)),
    'lock_seconds' => max(5, (int) env('SYSTEM_DEPLOY_RECEIVER_LOCK_SECONDS', 900)),
    'lock_key' => env('SYSTEM_DEPLOY_RECEIVER_LOCK_KEY', 'deploypier:receiver'),
    'queue_restart' => filter_var(env('SYSTEM_DEPLOY_RECEIVER_QUEUE_RESTART', true), FILTER_VALIDATE_BOOL),
];
`

const controllerTemplate = `
<?php

namespace App\Http\Controllers\Internal;

use App\Http\Controllers\Controller;
use App\Http\Requests\Internal\ReceiveDeployHookRequest;
use App\Services\Deploy\DeployHookReceiverService;
use Illuminate\Http\JsonResponse;

class DeployReceiveController extends Controller
{
    public function __invoke(
        ReceiveDeployHookRequest $request,
        DeployHookReceiverService $receiverService,
    ): JsonResponse {
        $result = $receiverService->handle($request);

        return response()->json($result['body'], $result['status']);
    }
}
`

const middlewareTemplate = `
<?php

namespace App\Http\Middleware;

use App\Services\Deploy\DeployHookSignatureService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

class EnsureValidDeployHookSignature
{
    public function __construct(
        private readonly DeployHookSignatureService $signatureService,
    ) {
    }

    public function handle(Request $request, \Closure $next): Response
    {
        $validation = $this->signatureService->validateRequest($request);

        if (! $validation['ok']) {
            return new JsonResponse($validation['body'], $validation['status']);
        }

        $request->attributes->set('deploy_hook_signature', $validation['data']);

        return $next($request);
    }
}
`

const requestTemplate = `
<?php

namespace App\Http\Requests\Internal;

use Illuminate\Foundation\Http\FormRequest;
use Illuminate\Validation\Rule;

class ReceiveDeployHookRequest extends FormRequest
{
    public function authorize(): bool
    {
        return true;
    }

    public function rules(): array
    {
        return [
            'operation' => ['required', 'string', Rule::in(['post_deploy_v1', 'prepare_release_v1', 'extract_release_v1'])],
            'environment' => ['nullable', 'string', 'max:80'],
            'app' => ['nullable', 'string', 'max:120'],
            'mode' => ['nullable', 'string', Rule::in(['release-based', 'in-place'])],
            'release_id' => ['required', 'string', 'max:120', 'regex:/^[A-Za-z0-9._\\/-]+$/'],
            'base_release_id' => ['nullable', 'string', 'max:120', 'regex:/^[A-Za-z0-9._\\/-]+$/'],
            'ref' => ['nullable', 'string', 'max:120', 'regex:/^[A-Za-z0-9._\\/-]+$/'],
            'commit' => ['nullable', 'string', 'max:64', 'regex:/^[a-f0-9]{7,64}$/i'],
            'triggered_at' => ['required', 'date'],
            'artifact' => ['nullable', 'array'],
            'artifact.sha256' => ['nullable', 'string', 'size:64', 'regex:/^[a-f0-9]{64}$/i'],
            'artifact.size' => ['nullable', 'integer', 'min:0'],
            'artifact.uploaded_path' => ['nullable', 'string', 'max:1000'],
            'artifact.archive_sha256' => ['nullable', 'string', 'size:64', 'regex:/^[a-f0-9]{64}$/i'],
            'artifact.archive_size' => ['nullable', 'integer', 'min:0'],
            'remove_paths' => ['nullable', 'array'],
            'remove_paths.*' => ['string', 'max:1000', 'regex:/^(?!\\.|\\/)(?!.*\\.\\.)(?!.*\\/\\.)(?!.*\\/$)[A-Za-z0-9_\\.\\-\\/]+$/'],
            'changed_paths' => ['nullable', 'array'],
            'changed_paths.*' => ['string', 'max:1000', 'regex:/^(?!\\.|\\/)(?!.*\\.\\.)(?!.*\\/\\.)(?!.*\\/$)[A-Za-z0-9_\\.\\-\\/]+$/'],
            'meta' => ['nullable', 'array'],
        ];
    }
}
`

const modelTemplate = `
<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;

class DeployHookExecution extends Model
{
    use HasFactory;

    public const STATUS_PROCESSING = 'processing';
    public const STATUS_SUCCESS = 'success';
    public const STATUS_FAILED = 'failed';

    protected $fillable = [
        'environment',
        'app_name',
        'operation',
        'mode',
        'release_id',
        'idempotency_key',
        'signature_key_id',
        'signature_version',
        'signature_scope',
        'signature_nonce',
        'signature_timestamp',
        'payload_hash',
        'status',
        'ref',
        'commit_sha',
        'request_payload',
        'command_results',
        'error_message',
        'started_at',
        'finished_at',
    ];

    protected function casts(): array
    {
        return [
            'signature_timestamp' => 'datetime',
            'request_payload' => 'array',
            'command_results' => 'array',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
        ];
    }
}
`

const signatureServiceTemplate = `
<?php

namespace App\Services\Deploy;

use Carbon\CarbonImmutable;
use Illuminate\Http\Request;
use Illuminate\Support\Str;

class DeployHookSignatureService
{
    public function validateRequest(Request $request): array
    {
        if (! (bool) config('deploypier.enabled', false)) {
            return $this->error('deploy_receiver_disabled', 'Receiver de deploy desativado na configuracao.', 503);
        }

        $configuredKeyId = trim((string) config('deploypier.key_id', ''));
        $configuredSecret = trim((string) config('deploypier.secret', ''));
        $configuredVersion = trim((string) config('deploypier.signature_version', 'v1'));
        $configuredScope = trim((string) config('deploypier.signature_scope', 'deploy:post-deploy'));
        $providedKeyId = trim((string) $request->header('X-Deploy-Key-Id', ''));
        $timestamp = trim((string) $request->header('X-Deploy-Timestamp', ''));
        $nonce = trim((string) $request->header('X-Deploy-Nonce', ''));
        $idempotencyKey = trim((string) $request->header('Idempotency-Key', ''));
        $version = trim((string) $request->header('X-Deploy-Signature-Version', ''));
        $scope = trim((string) $request->header('X-Deploy-Signature-Scope', ''));
        $signature = $this->normalizeSignature((string) $request->header('X-Deploy-Signature', ''));

        if (
            $configuredKeyId === ''
            || $configuredSecret === ''
            || $providedKeyId === ''
            || $timestamp === ''
            || $nonce === ''
            || $idempotencyKey === ''
            || $version === ''
            || $scope === ''
            || $signature === ''
        ) {
            return $this->error('invalid_signature', 'Headers obrigatorios da assinatura de deploy estao ausentes.', 401);
        }

        if (! hash_equals($configuredKeyId, $providedKeyId)) {
            return $this->error('invalid_signature', 'Key id do hook de deploy nao confere.', 401);
        }

        if (! hash_equals($configuredVersion, $version)) {
            return $this->error('invalid_signature', 'Versao da assinatura do hook de deploy nao confere.', 401);
        }

        if (! hash_equals($configuredScope, $scope)) {
            return $this->error('invalid_signature', 'Escopo da assinatura do hook de deploy nao confere.', 401);
        }

        if (! ctype_digit($timestamp)) {
            return $this->error('invalid_signature', 'Timestamp do hook de deploy eh invalido.', 401);
        }

        if (! Str::isAscii($nonce) || mb_strlen($nonce) > 120) {
            return $this->error('invalid_signature', 'Nonce do hook de deploy eh invalido.', 401);
        }

        $tolerance = max(5, (int) config('deploypier.replay_tolerance_seconds', 300));
        $timestampAt = CarbonImmutable::createFromTimestampUTC((int) $timestamp);
        $age = abs(now('UTC')->timestamp - $timestampAt->timestamp);

        if ($age > $tolerance) {
            return $this->error('signature_expired', 'Assinatura do hook de deploy fora da janela de tolerancia.', 401, [
                'tolerance_seconds' => $tolerance,
            ]);
        }

        $expectedSignature = $this->sign(
            keyId: $providedKeyId,
            idempotencyKey: $idempotencyKey,
            timestamp: $timestamp,
            nonce: $nonce,
            scope: $scope,
            version: $version,
            payload: $request->getContent(),
        );

        if (! hash_equals($expectedSignature, $signature)) {
            return $this->error('invalid_signature', 'Assinatura HMAC do hook de deploy nao confere.', 401);
        }

        return [
            'ok' => true,
            'status' => 200,
            'body' => [],
            'data' => [
                'key_id' => $providedKeyId,
                'idempotency_key' => $idempotencyKey,
                'timestamp' => $timestamp,
                'timestamp_at' => $timestampAt,
                'nonce' => $nonce,
                'version' => $version,
                'scope' => $scope,
                'signature' => $signature,
                'payload_hash' => hash('sha256', $this->canonicalPayload($request->getContent())),
            ],
        ];
    }

    public function sign(
        string $keyId,
        string $idempotencyKey,
        string $timestamp,
        string $nonce,
        string $scope,
        string $version,
        string $payload,
    ): string {
        $secret = trim((string) config('deploypier.secret', ''));
        $payloadHash = hash('sha256', $this->canonicalPayload($payload));

        return hash_hmac(
            'sha256',
            implode('.', [$version, $scope, $keyId, $timestamp, $nonce, $idempotencyKey, $payloadHash]),
            $secret,
        );
    }

    public function canonicalPayload(string $payload): string
    {
        $trimmedPayload = trim($payload);
        if ($trimmedPayload === '') {
            return '{}';
        }

        $decoded = json_decode($payload, true);
        if (json_last_error() !== JSON_ERROR_NONE) {
            return $trimmedPayload;
        }

        $encoded = json_encode($decoded, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);

        return is_string($encoded) ? $encoded : $trimmedPayload;
    }

    protected function normalizeSignature(string $signature): string
    {
        $trimmed = trim($signature);

        if (str_starts_with(strtolower($trimmed), 'sha256=')) {
            return substr($trimmed, 7);
        }

        return $trimmed;
    }

    protected function error(string $code, string $message, int $status, array $details = []): array
    {
        return [
            'ok' => false,
            'status' => $status,
            'body' => [
                'message' => $message,
                'error' => [
                    'code' => $code,
                    'message' => $message,
                    'details' => $details,
                ],
            ],
        ];
    }
}
`

const pipelineServiceTemplate = `
<?php

namespace App\Services\Deploy;

use Illuminate\Support\Facades\Artisan;
use RuntimeException;
use SplFileInfo;
use ZipArchive;

class DeployHookPipelineService
{
    public function run(): array
    {
        $results = [];

        $results[] = $this->runCommand('migrate', [
            '--force' => true,
            '--no-interaction' => true,
        ]);

        $results[] = $this->runCommand('optimize:clear', [
            '--no-interaction' => true,
        ]);

        $results[] = $this->runCommand('optimize', [
            '--no-interaction' => true,
        ]);

        if ($this->shouldRestartQueue()) {
            $results[] = $this->runCommand('queue:restart');
        } else {
            $results[] = [
                'command' => 'queue:restart',
                'parameters' => [],
                'exit_code' => 0,
                'output' => 'Queue restart skipped because queue driver is sync or restart is disabled.',
                'started_at' => now()->toIso8601String(),
                'finished_at' => now()->toIso8601String(),
                'skipped' => true,
            ];
        }

        return $results;
    }

    public function prepareRelease(string $releaseId, ?string $baseReleaseId = null, array $removePaths = []): array
    {
        $startedAt = now();
        $sourcePath = base_path();
        $sourceReleaseId = basename($sourcePath);
        $releaseRoot = dirname($sourcePath);
        $targetPath = $releaseRoot.DIRECTORY_SEPARATOR.$releaseId;

        if ($baseReleaseId !== null && trim($baseReleaseId) !== '' && trim($baseReleaseId) !== $sourceReleaseId) {
            throw new RuntimeException(sprintf(
                'A release ativa atual [%s] nao confere com a base esperada [%s].',
                $sourceReleaseId,
                trim($baseReleaseId),
            ));
        }

        if (file_exists($targetPath)) {
            throw new RuntimeException(sprintf('A release de destino [%s] ja existe no host.', $releaseId));
        }

        if (! is_dir($sourcePath)) {
            throw new RuntimeException('O diretorio da release ativa nao foi encontrado.');
        }

        $copiedFiles = $this->copyTree($sourcePath, $targetPath);
        $removedEntries = 0;
        foreach ($removePaths as $relativePath) {
            $removedEntries += $this->removeRelativePath($targetPath, (string) $relativePath);
        }

        return [[
            'command' => 'prepare_release',
            'parameters' => [
                'release_id' => $releaseId,
                'base_release_id' => $sourceReleaseId,
                'remove_paths_count' => count($removePaths),
            ],
            'exit_code' => 0,
            'output' => sprintf(
                'Release %s preparada a partir de %s com %d arquivos copiados e %d caminhos removidos.',
                $releaseId,
                $sourceReleaseId,
                $copiedFiles,
                $removedEntries,
            ),
            'started_at' => $startedAt->toIso8601String(),
            'finished_at' => now()->toIso8601String(),
        ]];
    }

    public function extractRelease(string $releaseId, ?string $expectedArchiveSha256 = null, ?int $expectedArchiveSize = null): array
    {
        $startedAt = now();
        $releasePath = dirname(base_path()).DIRECTORY_SEPARATOR.$releaseId;
        $archivePath = $releasePath.DIRECTORY_SEPARATOR.'release.zip';

        if (! is_file($archivePath)) {
            throw new RuntimeException(sprintf('O arquivo [%s] nao foi encontrado para extracao.', $archivePath));
        }

        if ($expectedArchiveSize !== null && filesize($archivePath) !== $expectedArchiveSize) {
            throw new RuntimeException('O tamanho do arquivo release.zip nao confere com o informado pela CLI.');
        }

        if ($expectedArchiveSha256 !== null && hash_file('sha256', $archivePath) !== strtolower($expectedArchiveSha256)) {
            throw new RuntimeException('O hash SHA-256 do arquivo release.zip nao confere com o informado pela CLI.');
        }

        $zip = new ZipArchive();
        $openStatus = $zip->open($archivePath);
        if ($openStatus !== true) {
            throw new RuntimeException(sprintf('Nao foi possivel abrir o arquivo [%s] (status %s).', $archivePath, (string) $openStatus));
        }

        try {
            for ($index = 0; $index < $zip->numFiles; $index++) {
                $entryName = (string) $zip->getNameIndex($index);
                $this->assertSafeArchiveEntry($entryName);
            }

            if (! $zip->extractTo($releasePath)) {
                throw new RuntimeException(sprintf('Falha ao extrair o arquivo [%s] em [%s].', $archivePath, $releasePath));
            }
        } finally {
            $zip->close();
        }

        if (! @unlink($archivePath)) {
            throw new RuntimeException(sprintf('Falha ao remover o arquivo temporario [%s] apos a extracao.', $archivePath));
        }

        return [[
            'command' => 'extract_release',
            'parameters' => [
                'release_id' => $releaseId,
            ],
            'exit_code' => 0,
            'output' => sprintf('Arquivo release.zip extraido com sucesso para a release %s.', $releaseId),
            'started_at' => $startedAt->toIso8601String(),
            'finished_at' => now()->toIso8601String(),
        ]];
    }

    protected function runCommand(string $command, array $parameters = []): array
    {
        $startedAt = now();
        $exitCode = Artisan::call($command, $parameters);
        $output = trim(Artisan::output());

        $result = [
            'command' => $command,
            'parameters' => $parameters,
            'exit_code' => $exitCode,
            'output' => $output,
            'started_at' => $startedAt->toIso8601String(),
            'finished_at' => now()->toIso8601String(),
        ];

        if ($exitCode !== 0) {
            throw new RuntimeException(sprintf(
                'Falha ao executar o comando [%s] (exit code %d). Output: %s',
                $command,
                $exitCode,
                $output !== '' ? $output : 'sem saída',
            ));
        }

        return $result;
    }

    protected function shouldRestartQueue(): bool
    {
        if (! (bool) config('deploypier.queue_restart', true)) {
            return false;
        }

        return (string) config('queue.default', 'sync') !== 'sync';
    }

    protected function copyTree(string $sourcePath, string $targetPath): int
    {
        $entries = new \RecursiveIteratorIterator(
            new \RecursiveDirectoryIterator($sourcePath, \FilesystemIterator::SKIP_DOTS),
            \RecursiveIteratorIterator::SELF_FIRST,
        );

        if (! is_dir($targetPath) && ! mkdir($targetPath, 0755, true) && ! is_dir($targetPath)) {
            throw new RuntimeException(sprintf('Nao foi possivel criar a release de destino [%s].', $targetPath));
        }

        $copiedFiles = 0;
        foreach ($entries as $entry) {
            if (! $entry instanceof SplFileInfo) {
                continue;
            }

            $relativePath = ltrim(str_replace('\\', '/', substr($entry->getPathname(), strlen($sourcePath))), '/');
            if ($relativePath === '') {
                continue;
            }

            $targetEntryPath = $targetPath.DIRECTORY_SEPARATOR.str_replace('/', DIRECTORY_SEPARATOR, $relativePath);
            if ($entry->isDir()) {
                if (! is_dir($targetEntryPath) && ! mkdir($targetEntryPath, 0755, true) && ! is_dir($targetEntryPath)) {
                    throw new RuntimeException(sprintf('Nao foi possivel criar o diretorio [%s].', $targetEntryPath));
                }
                continue;
            }

            $targetDir = dirname($targetEntryPath);
            if (! is_dir($targetDir) && ! mkdir($targetDir, 0755, true) && ! is_dir($targetDir)) {
                throw new RuntimeException(sprintf('Nao foi possivel preparar o diretorio [%s].', $targetDir));
            }

            if (! copy($entry->getPathname(), $targetEntryPath)) {
                throw new RuntimeException(sprintf('Nao foi possivel copiar [%s] para [%s].', $entry->getPathname(), $targetEntryPath));
            }
            $copiedFiles++;
        }

        return $copiedFiles;
    }

    protected function removeRelativePath(string $targetPath, string $relativePath): int
    {
        $normalized = $this->normalizeRelativePath($relativePath);
        if ($normalized === '') {
            return 0;
        }

        $fullPath = $targetPath.DIRECTORY_SEPARATOR.str_replace('/', DIRECTORY_SEPARATOR, $normalized);
        if (! file_exists($fullPath) && ! is_link($fullPath)) {
            return 0;
        }

        return $this->deletePath($fullPath);
    }

    protected function normalizeRelativePath(string $relativePath): string
    {
        $normalized = trim(str_replace('\\', '/', $relativePath));
        $normalized = ltrim($normalized, '/');
        if ($normalized === '' || str_contains($normalized, '..')) {
            throw new RuntimeException('Caminho relativo invalido recebido para operacao remota.');
        }

        return $normalized;
    }

    protected function deletePath(string $path): int
    {
        if (is_link($path) || is_file($path)) {
            if (! @unlink($path)) {
                throw new RuntimeException(sprintf('Nao foi possivel remover o arquivo [%s].', $path));
            }
            return 1;
        }

        if (! is_dir($path)) {
            return 0;
        }

        $removed = 0;
        $entries = array_diff(scandir($path) ?: [], ['.', '..']);
        foreach ($entries as $entry) {
            $removed += $this->deletePath($path.DIRECTORY_SEPARATOR.$entry);
        }

        if (! @rmdir($path)) {
            throw new RuntimeException(sprintf('Nao foi possivel remover o diretorio [%s].', $path));
        }

        return $removed + 1;
    }

    protected function assertSafeArchiveEntry(string $entryName): void
    {
        $normalized = trim(str_replace('\\', '/', $entryName));
        if ($normalized === '') {
            throw new RuntimeException('O arquivo release.zip contem uma entrada vazia.');
        }
        if (str_starts_with($normalized, '/') || str_contains($normalized, '../') || str_contains($normalized, '..\\') || str_ends_with($normalized, '/..') || $normalized === '..') {
            throw new RuntimeException(sprintf('Entrada insegura detectada no release.zip: [%s].', $entryName));
        }
    }
}
`

const receiverServiceTemplate = `
<?php

namespace App\Services\Deploy;

use App\Http\Requests\Internal\ReceiveDeployHookRequest;
use App\Models\DeployHookExecution;
use Illuminate\Contracts\Cache\Lock;
use Illuminate\Support\Facades\Cache;
use Throwable;

class DeployHookReceiverService
{
    public function __construct(
        private readonly DeployHookPipelineService $pipelineService,
    ) {
    }

    public function handle(ReceiveDeployHookRequest $request): array
    {
        $this->applyRequestTimeout();

        $signature = $request->attributes->get('deploy_hook_signature', []);
        $idempotencyKey = (string) ($signature['idempotency_key'] ?? '');
        $payloadHash = (string) ($signature['payload_hash'] ?? '');

        $existing = DeployHookExecution::query()
            ->where('idempotency_key', $idempotencyKey)
            ->first();

        if ($existing) {
            return $this->buildExistingExecutionResponse($existing, $payloadHash);
        }

        $lock = Cache::lock(
            (string) config('deploypier.lock_key', 'deploypier:receiver'),
            max(5, (int) config('deploypier.lock_seconds', 900)),
        );

        if (! $lock->get()) {
            return $this->error('deploy_locked', 'Ja existe um hook de deploy em execucao.', 409, [
                'lock_key' => (string) config('deploypier.lock_key', 'deploypier:receiver'),
            ]);
        }

        try {
            $execution = $this->createExecution($request, $signature);

            try {
                $commandResults = $this->runOperation($request);

                $execution->update([
                    'status' => DeployHookExecution::STATUS_SUCCESS,
                    'command_results' => $commandResults,
                    'error_message' => null,
                    'finished_at' => now(),
                ]);

                $execution->refresh();

                return [
                    'status' => 200,
                    'body' => $this->successfulResponse($execution, false),
                ];
            } catch (Throwable $exception) {
                report($exception);

                $execution->update([
                    'status' => DeployHookExecution::STATUS_FAILED,
                    'error_message' => mb_substr($exception->getMessage(), 0, 4000),
                    'finished_at' => now(),
                ]);

                $execution->refresh();

                return $this->error(
                    'deploy_hook_failed',
                    'Falha ao executar a operacao remota do DeployPier.',
                    500,
                    [
                        'execution' => $this->executionPayload($execution),
                        'step' => $this->failedStep($execution),
                    ],
                );
            }
        } finally {
            $this->releaseLock($lock);
        }
    }

    protected function runOperation(ReceiveDeployHookRequest $request): array
    {
        $validated = $request->validated();

        return match ((string) ($validated['operation'] ?? '')) {
            'prepare_release_v1' => $this->pipelineService->prepareRelease(
                releaseId: (string) $validated['release_id'],
                baseReleaseId: isset($validated['base_release_id']) ? (string) $validated['base_release_id'] : null,
                removePaths: is_array($validated['remove_paths'] ?? null) ? $validated['remove_paths'] : [],
            ),
            'extract_release_v1' => $this->pipelineService->extractRelease(
                releaseId: (string) $validated['release_id'],
                expectedArchiveSha256: isset($validated['artifact']['archive_sha256']) ? (string) $validated['artifact']['archive_sha256'] : null,
                expectedArchiveSize: isset($validated['artifact']['archive_size']) ? (int) $validated['artifact']['archive_size'] : null,
            ),
            default => $this->pipelineService->run(),
        };
    }

    protected function createExecution(ReceiveDeployHookRequest $request, array $signature): DeployHookExecution
    {
        $validated = $request->validated();
        $payload = $request->json()->all();

        return DeployHookExecution::query()->create([
            'environment' => (string) ($validated['environment'] ?? 'production'),
            'app_name' => (string) ($validated['app'] ?? 'laravel-app'),
            'operation' => (string) $validated['operation'],
            'mode' => (string) ($validated['mode'] ?? 'release-based'),
            'release_id' => (string) $validated['release_id'],
            'idempotency_key' => (string) $signature['idempotency_key'],
            'signature_key_id' => (string) $signature['key_id'],
            'signature_version' => (string) $signature['version'],
            'signature_scope' => (string) $signature['scope'],
            'signature_nonce' => (string) $signature['nonce'],
            'signature_timestamp' => $signature['timestamp_at'],
            'payload_hash' => (string) $signature['payload_hash'],
            'status' => DeployHookExecution::STATUS_PROCESSING,
            'ref' => isset($validated['ref']) ? trim((string) $validated['ref']) : null,
            'commit_sha' => isset($validated['commit']) ? strtolower(trim((string) $validated['commit'])) : null,
            'request_payload' => is_array($payload) ? $payload : [],
            'command_results' => [],
            'error_message' => null,
            'started_at' => now(),
            'finished_at' => null,
        ]);
    }

    protected function buildExistingExecutionResponse(DeployHookExecution $execution, string $payloadHash): array
    {
        if (! hash_equals((string) $execution->payload_hash, $payloadHash)) {
            return $this->error('idempotency_conflict', 'A chave de idempotencia ja foi usada com um payload diferente.', 409, [
                'execution_id' => $execution->id,
            ]);
        }

        if ($execution->status === DeployHookExecution::STATUS_PROCESSING) {
            return $this->error('deploy_in_progress', 'Ja existe um hook de pos-deploy em processamento para essa chave de idempotencia.', 409, [
                'execution_id' => $execution->id,
            ]);
        }

        if ($execution->status === DeployHookExecution::STATUS_FAILED) {
            return [
                'status' => 200,
                'body' => [
                    'ok' => false,
                    'status' => 'failed',
                    'operation' => (string) $execution->operation,
                    'execution_id' => (string) $execution->id,
                    'idempotency_key' => (string) $execution->idempotency_key,
                    'release_id' => (string) $execution->release_id,
                    'idempotent_replay' => true,
                    'message' => 'Hook de pos-deploy ja processado anteriormente com falha.',
                    'execution' => $this->executionPayload($execution),
                ],
            ];
        }

        return [
            'status' => 200,
            'body' => $this->successfulResponse($execution, true),
        ];
    }

    protected function executionPayload(DeployHookExecution $execution): array
    {
        return [
            'id' => $execution->id,
            'status' => (string) $execution->status,
            'environment' => (string) $execution->environment,
            'app' => (string) $execution->app_name,
            'operation' => (string) $execution->operation,
            'mode' => (string) $execution->mode,
            'release_id' => (string) $execution->release_id,
            'idempotency_key' => (string) $execution->idempotency_key,
            'signature_key_id' => (string) $execution->signature_key_id,
            'signature_version' => (string) $execution->signature_version,
            'signature_scope' => (string) $execution->signature_scope,
            'ref' => $execution->ref,
            'commit_sha' => $execution->commit_sha,
            'request_payload' => $execution->request_payload ?? [],
            'command_results' => $execution->command_results ?? [],
            'error_message' => $execution->error_message,
            'started_at' => optional($execution->started_at)->toIso8601String(),
            'finished_at' => optional($execution->finished_at)->toIso8601String(),
            'created_at' => optional($execution->created_at)->toIso8601String(),
        ];
    }

    protected function successfulResponse(DeployHookExecution $execution, bool $replayed): array
    {
        return [
            'ok' => true,
            'status' => 'completed',
            'operation' => (string) $execution->operation,
            'execution_id' => (string) $execution->id,
            'idempotency_key' => (string) $execution->idempotency_key,
            'release_id' => (string) $execution->release_id,
            'started_at' => optional($execution->started_at)->toIso8601String(),
            'finished_at' => optional($execution->finished_at)->toIso8601String(),
            'idempotent_replay' => $replayed,
            'warnings' => [],
            'execution' => $this->executionPayload($execution),
        ];
    }

    protected function failedStep(DeployHookExecution $execution): ?string
    {
        $results = $execution->command_results ?? [];
        if (! is_array($results)) {
            return null;
        }

        foreach ($results as $result) {
            if (is_array($result) && ((int) ($result['exit_code'] ?? 0) !== 0)) {
                return (string) ($result['command'] ?? '');
            }
        }

        return null;
    }

    protected function releaseLock(Lock $lock): void
    {
        try {
            $lock->release();
        } catch (Throwable) {
        }
    }

    protected function applyRequestTimeout(): void
    {
        $seconds = max(30, (int) config('deploypier.request_timeout_seconds', 900));
        if (function_exists('set_time_limit')) {
            @set_time_limit($seconds);
        }
    }

    protected function error(string $code, string $message, int $status, array $details = []): array
    {
        return [
            'status' => $status,
            'body' => [
                'message' => $message,
                'error' => [
                    'code' => $code,
                    'message' => $message,
                    'details' => $details,
                ],
            ],
        ];
    }
}
`

const migrationTemplate = `
<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('deploy_hook_executions', function (Blueprint $table) {
            $table->id();
            $table->string('environment', 80)->default('production')->index();
            $table->string('app_name', 120)->default('laravel-app');
            $table->string('operation', 80)->default('post_deploy_v1')->index();
            $table->string('mode', 24)->default('release-based');
            $table->string('release_id', 120)->index();
            $table->string('idempotency_key', 120)->unique();
            $table->string('signature_key_id', 120);
            $table->string('signature_version', 24)->default('v1');
            $table->string('signature_scope', 120)->default('deploy:post-deploy');
            $table->string('signature_nonce', 120);
            $table->timestamp('signature_timestamp');
            $table->string('payload_hash', 64);
            $table->string('status', 24)->default('processing')->index();
            $table->string('ref', 120)->nullable();
            $table->string('commit_sha', 64)->nullable();
            $table->json('request_payload')->nullable();
            $table->json('command_results')->nullable();
            $table->text('error_message')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();

            $table->index(['status', 'created_at']);
            $table->index(['environment', 'created_at']);
            $table->index(['release_id', 'created_at']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('deploy_hook_executions');
    }
};
`

const featureTestTemplate = `
<?php

use App\Models\DeployHookExecution;
use App\Services\Deploy\DeployHookSignatureService;
use Illuminate\Support\Facades\Artisan;
use ZipArchive;

beforeEach(function () {
    config()->set('deploypier.enabled', true);
    config()->set('deploypier.key_id', 'shared-host-key');
    config()->set('deploypier.secret', 'shared-host-secret');
    config()->set('deploypier.signature_version', 'v1');
    config()->set('deploypier.signature_scope', 'deploy:post-deploy');
    config()->set('deploypier.replay_tolerance_seconds', 300);
    config()->set('deploypier.request_timeout_seconds', 120);
    config()->set('deploypier.lock_seconds', 120);
    config()->set('deploypier.lock_key', 'deploypier:test-receiver');
    config()->set('deploypier.queue_restart', true);
    config()->set('queue.default', 'sync');
});

test('deploy hook receiver processes a valid request', function () {
    Artisan::shouldReceive('call')->once()->with('migrate', ['--force' => true, '--no-interaction' => true])->andReturn(0);
    Artisan::shouldReceive('output')->once()->andReturn('Migrated.');
    Artisan::shouldReceive('call')->once()->with('optimize:clear', ['--no-interaction' => true])->andReturn(0);
    Artisan::shouldReceive('output')->once()->andReturn('Cleared.');
    Artisan::shouldReceive('call')->once()->with('optimize', ['--no-interaction' => true])->andReturn(0);
    Artisan::shouldReceive('output')->once()->andReturn('Optimized.');

    $payload = [
        'operation' => 'post_deploy_v1',
        'release_id' => '20260714T120000Z',
        'triggered_at' => now('UTC')->toIso8601String(),
    ];

    $response = $this->withHeaders(deployHookHeaders($payload, 'scaffold-success-1'))
        ->postJson('/api/internal/deploy/receive', $payload);

    $response->assertOk()
        ->assertJsonPath('status', 'completed');

    $this->assertDatabaseHas('deploy_hook_executions', [
        'idempotency_key' => 'scaffold-success-1',
        'status' => DeployHookExecution::STATUS_SUCCESS,
    ]);
});

test('deploy hook receiver prepares a remote release clone', function () {
    @mkdir(base_path('config'), 0755, true);
    @mkdir(base_path('public'), 0755, true);
    file_put_contents(base_path('config/app.php'), '<?php return [];');
    file_put_contents(base_path('public/build-old.js'), 'old-build');

    $payload = [
        'operation' => 'prepare_release_v1',
        'release_id' => '20260716T101500Z',
        'base_release_id' => basename(base_path()),
        'remove_paths' => ['public/build-old.js'],
        'triggered_at' => now('UTC')->toIso8601String(),
    ];

    $response = $this->withHeaders(deployHookHeaders($payload, 'scaffold-prepare-1'))
        ->postJson('/api/internal/deploy/receive', $payload);

    $response->assertOk()
        ->assertJsonPath('status', 'completed');

    $targetRoot = dirname(base_path()).DIRECTORY_SEPARATOR.'20260716T101500Z';
    expect(is_file($targetRoot.'/config/app.php'))->toBeTrue();
    expect(file_exists($targetRoot.'/public/build-old.js'))->toBeFalse();
});

test('deploy hook receiver extracts release zip into target release', function () {
    $releaseRoot = dirname(base_path()).DIRECTORY_SEPARATOR.'20260716T103000Z';
    @mkdir($releaseRoot, 0755, true);

    $archivePath = $releaseRoot.DIRECTORY_SEPARATOR.'release.zip';
    $zip = new ZipArchive();
    expect($zip->open($archivePath, ZipArchive::CREATE | ZipArchive::OVERWRITE))->toBeTrue();
    $zip->addFromString('bootstrap/app.php', '<?php return [];');
    $zip->addFromString('public/build/app.js', 'console.log("ok");');
    $zip->close();

    $payload = [
        'operation' => 'extract_release_v1',
        'release_id' => '20260716T103000Z',
        'triggered_at' => now('UTC')->toIso8601String(),
        'artifact' => [
            'uploaded_path' => $releaseRoot,
            'archive_sha256' => hash_file('sha256', $archivePath),
            'archive_size' => filesize($archivePath),
        ],
    ];

    $response = $this->withHeaders(deployHookHeaders($payload, 'scaffold-extract-1'))
        ->postJson('/api/internal/deploy/receive', $payload);

    $response->assertOk()
        ->assertJsonPath('status', 'completed');

    expect(is_file($releaseRoot.'/bootstrap/app.php'))->toBeTrue();
    expect(is_file($releaseRoot.'/public/build/app.js'))->toBeTrue();
    expect(file_exists($archivePath))->toBeFalse();
});

function deployHookHeaders(array $payload, string $idempotencyKey): array
{
    $timestamp = (string) now('UTC')->timestamp;
    $nonce = 'nonce-'.$idempotencyKey;
    $body = json_encode($payload, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);

    $service = app(DeployHookSignatureService::class);

    return [
        'X-Deploy-Key-Id' => 'shared-host-key',
        'X-Deploy-Timestamp' => $timestamp,
        'X-Deploy-Nonce' => $nonce,
        'Idempotency-Key' => $idempotencyKey,
        'X-Deploy-Signature-Version' => 'v1',
        'X-Deploy-Signature-Scope' => 'deploy:post-deploy',
        'X-Deploy-Signature' => $service->sign(
            keyId: 'shared-host-key',
            idempotencyKey: $idempotencyKey,
            timestamp: $timestamp,
            nonce: $nonce,
            scope: 'deploy:post-deploy',
            version: 'v1',
            payload: is_string($body) ? $body : '{}',
        ),
    ];
}
`

const unitTestTemplate = `
<?php

use App\Services\Deploy\DeployHookSignatureService;
use Illuminate\Http\Request;
use Tests\TestCase;

uses(TestCase::class);

beforeEach(function () {
    config()->set('deploypier.enabled', true);
    config()->set('deploypier.key_id', 'shared-host-key');
    config()->set('deploypier.secret', 'shared-host-secret');
    config()->set('deploypier.signature_version', 'v1');
    config()->set('deploypier.signature_scope', 'deploy:post-deploy');
    config()->set('deploypier.replay_tolerance_seconds', 300);
});

test('deploy hook signature service validates signed request', function () {
    $payload = [
        'operation' => 'post_deploy_v1',
        'release_id' => '20260714T120000Z',
        'triggered_at' => now('UTC')->toIso8601String(),
    ];
    $timestamp = (string) now('UTC')->timestamp;
    $nonce = 'nonce-unit-valid-1';
    $body = json_encode($payload, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);

    $service = app(DeployHookSignatureService::class);

    $request = Request::create('/api/internal/deploy/receive', 'POST', server: [
        'CONTENT_TYPE' => 'application/json',
        'HTTP_X_DEPLOY_KEY_ID' => 'shared-host-key',
        'HTTP_X_DEPLOY_TIMESTAMP' => $timestamp,
        'HTTP_X_DEPLOY_NONCE' => $nonce,
        'HTTP_IDEMPOTENCY_KEY' => 'unit-valid-1',
        'HTTP_X_DEPLOY_SIGNATURE_VERSION' => 'v1',
        'HTTP_X_DEPLOY_SIGNATURE_SCOPE' => 'deploy:post-deploy',
        'HTTP_X_DEPLOY_SIGNATURE' => $service->sign(
            keyId: 'shared-host-key',
            idempotencyKey: 'unit-valid-1',
            timestamp: $timestamp,
            nonce: $nonce,
            scope: 'deploy:post-deploy',
            version: 'v1',
            payload: is_string($body) ? $body : '{}',
        ),
    ], content: is_string($body) ? $body : '{}');

    $validation = $service->validateRequest($request);

    expect($validation['ok'])->toBeTrue();
});
`

func renderLocawebBootstrapScript(ftpUser string) string {
	return fmt.Sprintf(`
#!/usr/bin/env bash
set -Eeuo pipefail

FTP_USER=%q
PHP_BIN="${PHP_BIN:-/usr/bin/php84}"
COMPOSER_PHAR="${COMPOSER_PHAR:-/home/${FTP_USER}/composer.phar}"
APP_ROOT="${APP_ROOT:-$(pwd)}"
BASH_PROFILE_PATH="${BASH_PROFILE_PATH:-$HOME/.bash_profile}"

mkdir -p "$(dirname "$BASH_PROFILE_PATH")"
touch "$BASH_PROFILE_PATH"

if ! grep -Fq 'alias php="/usr/bin/php84"' "$BASH_PROFILE_PATH"; then
  printf '\nalias php="/usr/bin/php84"\n' >> "$BASH_PROFILE_PATH"
fi

if ! grep -Fq "alias composer=\"/usr/bin/php84 /home/${FTP_USER}/composer.phar\"" "$BASH_PROFILE_PATH"; then
  printf 'alias composer="/usr/bin/php84 /home/%s/composer.phar"\n' "$FTP_USER" >> "$BASH_PROFILE_PATH"
fi

cd "$APP_ROOT"

"$PHP_BIN" -d disable_functions= "$COMPOSER_PHAR" install --no-dev --prefer-dist --optimize-autoloader

ROOT="$APP_ROOT"
STORAGE_LINK="$ROOT/public_html/storage"
TARGET_PATH="$ROOT/storage/app/public"

mkdir -p "$ROOT/public_html"

if [ -L "$STORAGE_LINK" ]; then
  rm "$STORAGE_LINK"
elif [ -e "$STORAGE_LINK" ]; then
  echo "Refusing to replace non-symlink path: $STORAGE_LINK" >&2
  exit 1
fi

ln -s "$TARGET_PATH" "$STORAGE_LINK"
ls -l "$STORAGE_LINK"

cat <<'EOF'

Checklist manual Locaweb:
1. Reabra a sessao shell ou rode: source "$BASH_PROFILE_PATH"
2. Confirme que o link public_html/storage existe e aponta para storage/app/public.
3. Rode deploypier doctor para criar e validar o bootstrap do public_html/index.php antes do primeiro deploy.

EOF
`, ftpUser, ftpUser)
}

const locawebStorageLinkScript = `
#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="${APP_ROOT:-$(pwd)}"
STORAGE_LINK="$ROOT/public_html/storage"
TARGET_PATH="$ROOT/storage/app/public"

mkdir -p "$ROOT/public_html"

if [ -L "$STORAGE_LINK" ]; then
  rm "$STORAGE_LINK"
elif [ -e "$STORAGE_LINK" ]; then
  echo "Refusing to replace non-symlink path: $STORAGE_LINK" >&2
  exit 1
fi

ln -s "$TARGET_PATH" "$STORAGE_LINK"
ls -l "$STORAGE_LINK"
`

func renderLocawebBootstrapDoc(ftpUser string) string {
	bt := "`"

	return fmt.Sprintf(
		"# Locaweb bootstrap\n\n"+
			"Este projeto recebeu scripts de bootstrap para tarefas iniciais e manutencao manual na Locaweb.\n\n"+
			"## Scripts gerados\n\n"+
			"- %[1]sscripts/locaweb/bootstrap-first-deploy.sh%[1]s\n"+
			"- %[1]sscripts/locaweb/fix-storage-link.sh%[1]s\n\n"+
			"## O que o bootstrap faz\n\n"+
			"1. Garante aliases no %[1]s~/.bash_profile%[1]s:\n"+
			"   - %[1]salias php=\"/usr/bin/php84\"%[1]s\n"+
			"   - %[1]salias composer=\"/usr/bin/php84 /home/%[2]s/composer.phar\"%[1]s\n"+
			"2. Executa o composer no formato exigido pela Locaweb:\n"+
			"   - %[1]sphp -d disable_functions= composer.phar install --no-dev --prefer-dist --optimize-autoloader%[1]s\n"+
			"3. Recria manualmente o link de storage com shell, recusando sobrescrever diretorio comum:\n"+
			"   - remove apenas o symlink %[1]spublic_html/storage%[1]s quando ele ja existir\n"+
			"   - cria %[1]sln -s storage/app/public public_html/storage%[1]s\n\n"+
			"## Uso sugerido\n\n"+
			"%[1]s%[1]s%[1]sbash\ncd /caminho/do/projeto\nbash scripts/locaweb/bootstrap-first-deploy.sh\n%[1]s%[1]s%[1]s\n\n"+
			"## Observacao sobre o front controller\n\n"+
			"No modo %[1]srelease-based%[1]s, o %[1]spublic_html/index.php%[1]s deve permanecer estavel e ler a release ativa a partir de %[1]s.deploypier/current.txt%[1]s.\n\n"+
			"Quando esse arquivo nao existir ainda, o %[1]sdeploypier doctor%[1]s cria um bootstrap remoto usando %[1]sruntime.app_root%[1]s e %[1]sruntime.current_pointer%[1]s.\n\n"+
			"Se o front controller ja estiver customizado pelo projeto, o DeployPier nao o sobrescreve. Nesse caso, use o exemplo gerado como referencia e preserve a logica do ponteiro.\n\n"+
			"O hook HTTP do Laravel nao e o lugar certo para esse bootstrap inicial porque:\n\n"+
			"- alias de shell continuam sendo contexto do usuario, nao de um request HTTP;\n"+
			"- %[1]scomposer install%[1]s ainda pode ser util em manutencao manual, antes da app estar operacional;\n"+
			"- %[1]sstorage:link%[1]s via Artisan depende de funcoes PHP desabilitadas nesse ambiente;\n"+
			"- recriar o symlink de storage continua sendo uma tarefa melhor resolvida no shell.\n",
		bt, ftpUser,
	)
}

func renderLocawebDeployYAML(projectName string, ftpUser string) string {
	return fmt.Sprintf(`project:
  name: %q
  framework: "laravel"
  root: "."

build:
  php_command: "composer install --no-dev --prefer-dist --optimize-autoloader"
  node_command: "npm ci && npm run build"
  include:
    - "app/**"
    - "bootstrap/**"
    - "config/**"
    - "database/**"
    - "public/**"
    - "resources/**"
    - "routes/**"
    - "vendor/**"
  exclude:
    - ".env*"
    - ".git/**"
    - "docs/**"
    - "node_modules/**"
    - "storage/**"
    - "tests/**"

release:
  directory: "./.deploypier/releases"
  retain: 5
  upload_mode: "archive"

transport:
  kind: "ftps"
  protocol: "ftps"
  host: ""
  port: 21
  user: %q
  path: "/"
  known_hosts: ""
  allow_insecure: false

remote:
  app_root: "/app"
  public_root: "/public_html"
  layout: "release-based"

runtime:
  app_root: "/home/%s/app"
  current_pointer: "/home/%s/.deploypier/current.txt"

post_deploy:
  mode: "manual"
  remote_ops: "auto"
  hook_url_env: "DEPLOY_HOOK_URL"
  key_id_env: "DEPLOY_HOOK_KEY_ID"
  secret_env: "DEPLOY_HOOK_SECRET"
  request_timeout_env: "DEPLOY_HOOK_TIMEOUT"
  smoke_url: ""

state:
  file: "./.deploypier/state.json"

activation:
  kind: "pointer"
  current_pointer: "/.deploypier/current.txt"
`, projectName, ftpUser, ftpUser, ftpUser)
}

func renderLocawebDeployEnvExample(ftpUser string) string {
	return fmt.Sprintf(`# Copy this file to .deploy.env and keep the real file out of version control.
DEPLOY_HOST=
DEPLOY_PORT=21
DEPLOY_USER=%s
DEPLOY_PASSWORD=
DEPLOY_PRIVATE_KEY=
DEPLOYPIER_TRANSPORT_KNOWN_HOSTS=
DEPLOY_REMOTE_APP_ROOT=/app
DEPLOY_REMOTE_PUBLIC_ROOT=/public_html
DEPLOY_RUNTIME_APP_ROOT=/home/%s/app
DEPLOY_RUNTIME_CURRENT_POINTER=/home/%s/.deploypier/current.txt
DEPLOY_HOOK_URL=
DEPLOY_HOOK_KEY_ID=
DEPLOY_HOOK_SECRET=
DEPLOY_HOOK_TIMEOUT=10m
`, ftpUser, ftpUser, ftpUser)
}

func renderLocawebPublicIndexExample(ftpUser string) string {
	return fmt.Sprintf(`<?php
declare(strict_types=1);

// These paths are runtime filesystem paths used by PHP, not FTP transfer paths.
$basePath = '/home/%s/app';
$pointerFile = '/home/%s/.deploypier/current.txt';

$releaseId = trim((string) @file_get_contents($pointerFile));

if ($releaseId === '') {
    http_response_code(503);
    echo 'DeployPier: current release pointer is empty.';
    exit(1);
}

$releaseRoot = $basePath.'/releases/'.$releaseId;
$maintenance = $releaseRoot.'/storage/framework/maintenance.php';
$autoload = $releaseRoot.'/vendor/autoload.php';
$bootstrap = $releaseRoot.'/bootstrap/app.php';

if (is_file($maintenance)) {
    require $maintenance;
}

if (! is_file($autoload) || ! is_file($bootstrap)) {
    http_response_code(503);
    echo 'DeployPier: active release is incomplete.';
    exit(1);
}

require $autoload;
$app = require_once $bootstrap;
$app->usePublicPath(__DIR__);

return $app;
`, ftpUser, ftpUser)
}

func renderLocawebDeployGuide(projectName string, ftpUser string) string {
	return fmt.Sprintf(
		"# Deploy Locaweb\n\n"+
			"Arquivos gerados para o projeto %s com usuario FTP %s.\n\n"+
			"## Arquivos\n\n"+
			"- deploy.yml\n"+
			"- .deploy.env.example\n"+
			"- docs/deploypier-public-index.php.example\n"+
			"- scripts/locaweb/bootstrap-first-deploy.sh\n"+
			"- scripts/locaweb/fix-storage-link.sh\n\n"+
			"## Ordem sugerida\n\n"+
			"1. Gere o receiver Laravel:\n\n"+
			"    deploypier install-laravel-hook -project-root .\n\n"+
			"2. Gere o bootstrap Locaweb:\n\n"+
			"    deploypier install-locaweb-bootstrap -project-root . -ftp-user %s\n\n"+
			"3. Execute o bootstrap inicial no shell da hospedagem.\n\n"+
			"4. Copie .deploy.env.example para .deploy.env e preencha as variaveis locais.\n\n"+
			"5. Inspecione os paths remotos sugeridos antes do primeiro push:\n\n"+
			"    deploypier inspect-remote -config ./deploy.yml\n\n"+
			"6. Rode:\n\n"+
			"    deploypier doctor -config ./deploy.yml\n"+
			"    deploypier plan -config ./deploy.yml\n\n"+
			"7. Rode o primeiro deploy:\n\n"+
			"    deploypier push -config ./deploy.yml\n\n"+
			"## Observacoes\n\n"+
			"- O deploy assume que os arquivos publicos do Laravel ficam em public_html.\n"+
			"- transport.path, remote.app_root, remote.public_root e activation.current_pointer sao paths de transporte usados pelo FTP/SFTP.\n"+
			"- runtime.app_root e runtime.current_pointer sao paths absolutos usados pelo PHP dentro do public_html/index.php.\n"+
			"- Com post_deploy.remote_ops=auto, releases posteriores podem ser preparadas via Laravel para enviar so o delta de arquivos alterados.\n"+
			"- Se existir env.production ou .env.production na raiz local, o primeiro push usa esse arquivo apenas para semear o .env remoto quando a hospedagem ainda nao tiver um .env.\n"+
			"- DEPLOY_HOOK_TIMEOUT controla o timeout HTTP da CLI para o receiver; SYSTEM_DEPLOY_RECEIVER_REQUEST_TIMEOUT_SECONDS controla o tempo maximo do request no Laravel.\n"+
			"- Quando o path real da conta diferir do padrao /home/<ftp_user>, o inspect-remote ajuda a sugerir os paths de transporte. Os paths de runtime ainda podem precisar de confirmacao manual.\n"+
			"- O hook HTTP serve para pos-deploy da app ja operacional; bootstrap inicial continua no shell.\n"+
			"- O public_html/index.php deve ser estavel e ler a release ativa a partir de .deploypier/current.txt.\n"+
			"- Se o index.php remoto nao existir, deploypier doctor cria o bootstrap automaticamente.\n"+
			"- O DeployPier nao sobrescreve automaticamente um index.php ja customizado pelo projeto.\n",
		projectName, ftpUser, ftpUser,
	)
}
