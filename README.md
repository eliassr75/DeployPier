# DeployPier

CLI standalone em Go para deploy de projetos Laravel em shared hosting.

A proposta do `DeployPier` é simples: substituir o deploy manual via FTP por um fluxo repetível, auditável e mais seguro, sem exigir VPS, Docker no host ou shell avançado no servidor.

O foco do projeto é o cenário clássico de hospedagem compartilhada, incluindo provedores como a Locaweb, onde normalmente existem FTPS/SFTP, `public_html` fixo e pouca ou nenhuma automação nativa.

## Posicionamento

O `DeployPier` não tenta transformar shared hosting em VPS. A ideia é trabalhar com as limitações reais desse ambiente e ainda assim entregar:

- build local
- release versionada
- manifesto com integridade
- upload remoto via FTPS ou SFTP
- ativação remota
- rollback de código
- hook Laravel assinado para pós-deploy

## Status atual

O MVP já está funcional para o fluxo principal de deploy e já cobre a espinha dorsal da publicação de uma aplicação Laravel nesse tipo de hospedagem.

Já implementado:

- `doctor`, `plan`, `build`, `push` e `rollback`
- upload remoto real via `ftps`
- upload remoto real via `sftp`
- `ftp` puro apenas com opt-in explícito por `allow_insecure: true`
- lock remoto por diretório
- validação remota do manifesto com hash por arquivo
- ativação remota `release-based`
- fallback explícito para `in-place` quando `remote.layout=auto` e o swap falha
- scaffold do receiver Laravel
- scaffold de bootstrap/documentação para Locaweb
- política de auto-migration fail-closed e bem mais restrita

## Conceitos rápidos

Antes de usar a CLI, vale alinhar três termos:

- `build`: é a etapa local que roda Composer e Node, monta a pasta da release e gera o `manifest.json`
- `release`: é um pacote versionado do seu projeto, gerado automaticamente pela CLI com um `release_id` no formato de timestamp
- `ativação`: é o momento em que a release enviada passa a ser a versão pública do site

Na prática, `ativar` significa trocar o que está servindo em `public_html` para apontar para a release nova.

Você não precisa informar `release` manualmente no fluxo normal. O comportamento padrão é:

1. você roda `build`
2. a CLI gera a release mais recente
3. você roda `push`
4. o `push` usa automaticamente a última release gerada

O parâmetro `-release` existe só para casos específicos, como:

- publicar uma release antiga que já foi gerada localmente
- repetir o envio de uma release específica
- fazer rollback direcionado

## Primeiro deploy

Se você quer usar a ferramenta sem pensar muito na arquitetura primeiro, o caminho mais comum é este:

### 1. Gerar os arquivos iniciais do projeto

```bash
deploypier init-locaweb -project-root . -ftp-user meuusuarioftp
deploypier install-laravel-hook -project-root .
deploypier install-locaweb-bootstrap -project-root . -ftp-user meuusuarioftp
```

Isso gera:

- `deploy.yml`
- `.deploy.env.example`
- integração Laravel para o hook de pós-deploy
- scripts de bootstrap/manual para Locaweb

### 2. Preparar o ambiente local

Copie `.deploy.env.example` para `.deploy.env` e preencha as credenciais e paths do host.

### 3. Validar a configuração

```bash
deploypier doctor -config ./deploy.yml
```

### 4. Gerar a release local

```bash
deploypier build -config ./deploy.yml
```

Esse comando:

- roda o build local
- gera uma pasta em `.deploypier/releases/<release_id>`
- cria o `manifest.json`

### 5. Publicar

```bash
deploypier push -config ./deploy.yml
```

Sem passar `-release`, a CLI usa automaticamente a última release gerada no passo anterior.

### 6. Se precisar voltar

```bash
deploypier rollback -config ./deploy.yml
```

Esse comando tenta reativar a release anterior registrada no estado remoto.

## Como o deploy funciona

No modo padrão `release-based`, o fluxo é:

1. build local com Composer e Node
2. empacotamento por allowlist
3. geração de `manifest.json`
4. upload da release para `app/releases/<release_id>`
5. verificação remota do manifesto
6. montagem de um novo `public_html` temporário
7. geração de um `index.php` que aponta para a release ativa
8. swap remoto por rename
9. atualização do estado remoto e do ponteiro de release atual
10. hook Laravel assinado, quando `post_deploy.mode=auto`

No rollback, a CLI reativa a release anterior registrada no estado remoto e recompõe o `public_html` com os assets daquela release.

## Quando usar `-release`

Você só precisa informar `-release` manualmente quando quiser fugir do fluxo padrão.

Exemplo:

```bash
deploypier push -config ./deploy.yml -release 20260715T101500Z
```

Casos típicos:

- você já gerou mais de uma release local e quer escolher exatamente qual publicar
- você quer republicar uma release específica
- você quer testar uma release sem gerar outra nova antes

Se você está fazendo o fluxo normal de `build` seguido de `push`, não precisa informar nada manualmente.

## Por que Go

Go foi escolhido como base porque permite distribuir um binário único para Windows, Linux e macOS, sem exigir Node.js ou PHP na máquina do operador.

## Requisitos

Para compilar o projeto a partir do código-fonte hoje:

- Go 1.26

Para usar a CLI em um projeto Laravel:

- Git local
- Composer local
- Node.js e npm quando houver build frontend
- acesso FTPS ou SFTP ao host

## Instalação

```bash
go build -o deploypier .
```

Para rodar em desenvolvimento:

```bash
go run . help
```

## Comandos

### `doctor`

Valida configuração, paths locais, transporte remoto, capacidade de rename e status do pós-deploy.

```bash
deploypier doctor -config ./deploy.yml
```

### `plan`

Mostra o plano atual sem alterar nada, incluindo estratégia de layout e modo de pós-deploy.

```bash
deploypier plan -config ./deploy.yml
```

### `build`

Gera uma release local pronta para envio.

```bash
deploypier build -config ./deploy.yml
```

### `push`

Executa upload, verificação remota, ativação e pós-deploy quando configurado.

```bash
deploypier push -config ./deploy.yml
```

```bash
deploypier push -config ./deploy.yml -release 20260715T101500Z
```

```bash
deploypier push -config ./deploy.yml -skip-activate
```

### `rollback`

Reativa a release anterior registrada no estado remoto ou uma release informada manualmente.

```bash
deploypier rollback -config ./deploy.yml
```

```bash
deploypier rollback -config ./deploy.yml -release 20260715T101500Z
```

### `install-laravel-hook`

Gera a estrutura Laravel para receber o hook assinado de pós-deploy.

```bash
deploypier install-laravel-hook -project-root /path/to/app
```

### `install-locaweb-bootstrap`

Gera scripts e documentação para bootstrap inicial e manutenção manual na Locaweb.

```bash
deploypier install-locaweb-bootstrap -project-root /path/to/app -ftp-user meuusuarioftp
```

### `init-locaweb`

Gera `deploy.yml`, `.deploy.env.example` e a documentação inicial para um projeto Laravel hospedado nesse cenário.

```bash
deploypier init-locaweb -project-root /path/to/app -ftp-user meuusuarioftp
```

## Exemplo de `deploy.yml`

```yaml
project:
  name: "example-laravel-app"
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

transport:
  kind: "ftps"
  protocol: "ftps"
  host: ""
  port: 21
  user: ""
  path: "/home/ftp-user"
  known_hosts: ""
  allow_insecure: false

remote:
  app_root: "/home/ftp-user/app"
  public_root: "/home/ftp-user/public_html"
  layout: "release-based"

post_deploy:
  mode: "manual"
  hook_url_env: "DEPLOY_HOOK_URL"
  key_id_env: "DEPLOY_HOOK_KEY_ID"
  secret_env: "DEPLOY_HOOK_SECRET"
  smoke_url: ""

state:
  file: "./.deploypier/state.json"

activation:
  kind: "pointer"
  current_pointer: "/home/ftp-user/.deploypier/current.txt"
```

O arquivo completo de exemplo está em [deploy.yml.example](./deploy.yml.example).

## Variáveis de ambiente

Os segredos ficam fora do `deploy.yml`.

```bash
DEPLOY_HOST=
DEPLOY_PORT=21
DEPLOY_USER=
DEPLOY_PASSWORD=
DEPLOY_PRIVATE_KEY=
DEPLOY_REMOTE_APP_ROOT=
DEPLOY_REMOTE_PUBLIC_ROOT=
DEPLOY_HOOK_URL=
DEPLOY_HOOK_KEY_ID=
DEPLOY_HOOK_SECRET=
```

Para SFTP, você também pode apontar o arquivo de `known_hosts` via:

```bash
DEPLOYPIER_TRANSPORT_KNOWN_HOSTS=
```

Você pode usar `.deploy.env` localmente. A CLI carrega esse arquivo automaticamente quando ele estiver ao lado do `deploy.yml`.

## Hook Laravel

O receiver gerado expõe o endpoint:

```text
POST /api/internal/deploy/receive
```

Headers esperados:

- `Idempotency-Key`
- `X-Deploy-Key-Id`
- `X-Deploy-Timestamp`
- `X-Deploy-Nonce`
- `X-Deploy-Signature-Version`
- `X-Deploy-Signature-Scope`
- `X-Deploy-Signature`

O pipeline de pós-deploy executado pelo receiver é:

- `migrate --force`
- `optimize:clear`
- `optimize`
- `queue:restart` quando aplicável

## Política de migrations

O default público recomendado é:

```yaml
post_deploy:
  mode: "manual"
```

Quando `mode=auto`, a CLI só aceita migrations bem aditivas e curtas. Se o diff não puder ser avaliado, ou se aparecer qualquer migration fora da allowlist, o caminho automático é bloqueado antes da promoção.

Em `mode=manual`, o deploy de código pode seguir, mas o resultado volta como `needs_manual_migration` quando houver migration detectada.

## Locaweb

O projeto inclui um fluxo específico para Locaweb porque esse cenário motivou a ferramenta.

O bootstrap gerado cobre tarefas que normalmente acabam sendo feitas manualmente na primeira publicação:

- instalar aliases no shell
- usar `composer.phar` manualmente quando necessário
- recriar o symlink `public_html/storage`

No modo `release-based`, o `DeployPier` passa a gerenciar `public_html/index.php` durante a ativação. Na prática, o front controller deixa de ser um passo manual do deploy normal.

## Segurança

- prefira `ftps` ou `sftp`
- `ftp` puro exige `allow_insecure: true`
- SFTP usa verificação de host key via `known_hosts`, a menos que você force modo inseguro
- FTPS usa validação TLS por padrão
- o hook Laravel usa HMAC com janela de replay, nonce e idempotência
- segredos não aparecem em `deploy.yml`

## Testes

Para validar a CLI:

```bash
go test ./...
```

Para validar o receiver Laravel no projeto integrado:

```bash
php artisan test tests/Feature/Infrastructure/DeployHookReceiverTest.php tests/Unit/Deploy/DeployHookSignatureServiceTest.php
```

## Licença

MIT. O arquivo [LICENSE](./LICENSE) acompanha o repositório.
