# CherryPickレベルロール互換設計

## 目的

CherryPickのレベルロール機能をmk-goと専用frontendへ移植し、CherryPick由来DBから最終構成へ片道で移行できるようにする。

対象は`manualLevel`、経験値、level計算、level別policy、管理UI、ユーザー向け表示、プロフィール上のrole badge非表示までとする。

## 互換方針

- 保証対象は最終構成の`mk-go + Misaki-Project frontend`だけとする。
- 旧CherryPick frontendとmk-go、新frontendとCherryPick backendのcross-combinationは保証しない。
- mk-go切替後に同じDBをCherryPickへ戻すrollback互換は保証しない。
- 既存CherryPickデータはmigrationで書き換えず、同名columnと同じJSON shapeを読み取る。
- 切替失敗時は保持したsource dumpから環境を再構築する。移行後DBへ破壊的down migrationを実行しない。

## Repository構成

### Backend

- 実装repository: `Misaki0331/mk`
- PR base: `Misaki-Project/mk:Misaki-develop`
- reference: `Misaki-Project/cherrypick:Misaki-Stable`の`b30826d8ae`
- `shiroha-a/mk`と`shiroha-a/misskey-ts`はread-onlyとする。

### Frontend

- `shiroha-a/misskey-ts`を`Misaki-Project/misskey-ts`へOrganization forkする。
- 現在のsubmodule HEAD `ff25eac144c64d3ca1a06862547f7b101d315f98`を基点とする。
- `misskey-dev/misskey`や`Misaki-Project/misskey-tempura`から作り直さない。
- CherryPickの旧frontend fileはreferenceとして読むが、Misskey 2026.7の現在構造へ再実装する。
- mk-goの`third_party/misskey` pointerはfrontend側3 PR完了後の確定SHAへ更新する。

## DB contract

既存の`000012_role`は編集せず、新規mk-go migrationで次を追加する。

| 対象 | 型・制約 |
|---|---|
| `role_target_enum` | `manualLevel`を追加 |
| `role.levelPolicies` | `jsonb NOT NULL DEFAULT '{}'::jsonb` |
| `role_assignment.experience` | `bigint NULL` |
| experience index | `role_assignment(experience)` |
| `role.canHideProfileByUser` | `boolean NOT NULL DEFAULT false` |
| `role_assignment.isHideProfile` | `boolean NULL` |
| `role.policies` default | `'{}'::jsonb` |

CherryPick modelでは`experience`が`integer`、migrationでは`bigint`になっている。移行時のschemaをsource of truthとして`bigint`を採用する。

### Idempotency

- enum value、column、indexが存在する場合はno-opにする。
- fresh mk-go DBとCherryPick import済みDBの両方へ同じmigrationを適用できるようにする。
- schema名だけでなくcolumn型、nullable、defaultも検証する。名前が同じでshapeが違う場合は自動変更せず停止する。
- migration 2回目がno-changeになることをtestで固定する。

### Preflight

migration前に次を検証する。

- `manualLevel` roleだけがlevel role用dataを持つこと。
- `levelPolicies`がobjectで、`baseLevel`と`experiencePolicies`を解釈できること。
- experience policyの`type`が`const`、`linear`、`exponential`のいずれかであること。
- `level`、`base`、`additional`、`exponential`が許容範囲にあること。
- assignment experienceが非負でJavaScript safe integer以下であること。
- `isHideProfile=true`のassignmentが`canHideProfileByUser=true`のroleを参照すること。

不一致時は固定categoryだけを返し、ID、値、件数を出力せずmigrationを開始しない。

### Down migration

project規約に従いdown fileは用意するが、`manualLevel` role、experience、level policy、profile hide dataが存在する場合は固定errorで停止する。dataが存在しない場合だけ追加column・index・enum valueを除去できる。片道移行方針のため、本番復旧手段としてdown migrationを使用しない。

## Backend model

### Role

- `RoleTargetManualLevel = "manualLevel"`
- `LevelPolicies`
- `CanHideProfileByUser`
- `Policies`内の`policyAsLevel`

`LevelPolicies`は次のshapeを持つ。

```text
baseLevel: integer
experiencePolicies:
  - level: integer
    type: const | linear | exponential
    base: number
    additional?: number
    exponential?: number
```

### RoleAssignment

- `Experience *int64`
- `IsHideProfile *bool`

DBの`bigint`を受けるが、外部APIで扱う値は`0..Number.MAX_SAFE_INTEGER`へ制限する。

## Level engine

level計算は`internal/core/role`内の副作用を持たないcomponentへ分離する。

入力:

- assignment experience
- base level
- ordered experience policies

出力:

- current level
- current level内のexperience
- next levelに必要なexperience
- total experience
- minimum level
- maximum level

### 計算方式

- `const`: 各levelで固定experienceを要求する。
- `linear`: `base + additional * level`を各levelの要求値とする。
- `exponential`: CherryPickの`calculateExponentialSum`と同じ累積式を使用する。
- level policy境界では前policyの消費experienceを差し引いて次へ進む。
- 最大levelではnext-level値を返さない。
- overflow、NaN、負値、不正係数は受け入れずfail-closedにする。

CherryPick実装から生成したsynthetic golden caseでconst・linear・exponential・複数policy境界・最大levelを固定する。本番由来値はfixtureへ使用しない。

## Policy aggregation

`manualLevel` roleでは、通常の固定policyに加えて`policyAsLevel`を評価する。

対応mode:

- `base`: instance defaultを使用する。
- `const`: 指定値を使用する。
- `multiplier`: level差分に応じて`base + additional * level`を使用する。

複数roleが同じpolicyへ寄与する場合は、既存のpriority規則を維持する。同priorityの決定順序を固定し、Go map iterationへ依存させない。boolean、number、stringなどpolicy固有型を検証し、不正値はそのroleの寄与を無視するのではなくrequestを固定errorで失敗させる。

## Experience更新

新しいcore operationは次を受け取る。

- user ID
- role ID
- mode: `set`、`add`、`multiplier`
- value
- `assignForce`
- 任意のmoderation note

更新は1 transactionで行う。

1. roleとassignment rowをlockする。
2. role targetが`manualLevel`であることを検証する。
3. assignment不存在時は`assignForce`を要求する。
4. modeを適用する。
5. 結果を`0..Number.MAX_SAFE_INTEGER`へclampする。
6. roleの`lastUsedAt`を更新する。
7. commit後にuser role cacheをinvalidateする。
8. internal eventとmoderation logを発行する。

同時更新でlost updateが起きないことをDB-backed testで確認する。

## Profile role hide

`roles/profile-hide`は認証済みユーザー本人だけが呼べる。

- public roleが存在しない場合: `NO_SUCH_ROLE`
- `canHideProfileByUser=false`: `CANNOT_HIDE_THIS_ROLE`
- assignmentが存在しない場合: `NO_SUCH_ROLE`
- 成功時: assignmentの`isHideProfile`を更新しcacheをinvalidateする。

プロフィール、role badge、public user entityではhidden assignmentを表示しない。本人・moderator向け管理responseには状態を含める。

## API contract

### 追加

- `admin/roles/change-exp`
- `roles/profile-hide`

### 変更

- `admin/roles/create`: `manualLevel`と`levelPolicies`を受理
- `admin/roles/update`: `manualLevel`と`levelPolicies`を受理
- `admin/roles/list` / `show`: `levelPolicies`、`canHideProfileByUser`
- `roles/list` / `show`: public level role情報
- `roles/users`: `manualLevel`ではexperience降順
- `admin/show-user`: `roleAssigns[].experience`、`isHideProfile`
- user entity: roleごとの`experience`とhide capability

`experience` responseはCherryPickと同じfieldを持つ。

```text
currentLevel
currentExp
nextLevelExp
totalExp
minLevel
maxLevel
```

request validation、permission、error code・error ID、rate limitはCherryPick referenceに合わせる。既存manual/conditional roleのresponse shapeと動作を変更しない。

## Cache・event・監査

- role assignment cacheはexperienceまたはhide state更新後にuser単位でinvalidateする。
- role definition更新時はrole cacheと影響user cacheをinvalidateする。
- event payloadはmodel pointerを共有せず、更新後のimmutable dataを渡す。
- experience変更は`changeExperienceRole`相当のmoderation logへ記録する。
- profile hideは本人操作として監査可能な固定categoryを記録するが、公開ログへ内部値を出さない。

## Frontend設計

### Generated contract・i18n

- `Role.target`へ`manualLevel`
- `Role.levelPolicies`
- `Role.experience`
- `Role.canHideProfileByUser`
- assignmentの`experience`、`isHideProfile`
- `admin/roles/change-exp`
- `roles/profile-hide`
- `_role.manualLevel`、`manualLevelRoles`、`levelPolicies`
- `_experience`配下の計算方式・値・操作文言

### Admin editor

CherryPickの1833行版`roles.editor.vue`は移植しない。Misskey 2026.7の`roles.policy-editor.vue`とfolder componentへ次を追加する。

- target選択の`manualLevel`
- base/max level editor
- experience policyのsortable editor
- const・linear・exponential入力
- policyごとのlevel条件editor
- create/update payload生成
- readonly preview

### Admin user・経験値操作

- manual level roleのassignmentと現在levelを表示する。
- `set`、`add`、`multiplier`操作を提供する。
- 値と任意noteを確認dialog後に送信する。
- 操作後はuser詳細とrole listを再取得する。

### User UI

- role preview・tooltipへlevelとexperience progressを表示する。
- profile badgeにlevelを表示する。
- public role member listへmanual level roleを含める。
- roleが許可する場合、本人がprofile badgeの表示・非表示を切り替えられる。
- hidden roleをanonymous profileへ表示しない。

## Plugin拡張との関係

`shiroha-a/mk#2585`は将来、同種機能をpluginとして実装しやすくする提案である。本互換実装は既存CherryPick DB・APIとの完全互換が必要なため、Issueの実装を待たずcore機能として進める。plugin公開面へ内部role modelを露出しない。

## PR分割

### Backend PR 1: Schema・model

- migration・preflight
- model type
- repository round-trip
- fresh/imported/no-op migration tests

### Backend PR 2: Level engine・policy

- level計算
- policy interpolation
- experience update transaction
- cache invalidation・event
- unit/DB concurrency tests

### Backend PR 3: API・integration

- create/update/change-exp/profile-hide
- entity response・member ordering・moderation log
- frontend完了後のsubmodule pointer更新
- imported DB Docker E2E

Backend PR 3はAPI contractを先に確定したdraftとして公開し、frontend PR 3完了後にsubmodule pointerと最終E2Eを追加してmergeする。

### Frontend PR 1: Contract・i18n

- generated API types
- locale
- type fixtures

### Frontend PR 2: Admin editor

- level editor components
- role create/update UI
- admin role member UI

### Frontend PR 3: User UI

- experience操作
- preview・profile・explore
- profile hide
- frontend E2E

## Cutover

1. source側の書込みを停止する。
2. dumpを取得し、hashをprocess内だけで確認する。
3. isolated PostgreSQL 18へrestoreする。
4. compatibility preflightを実行する。
5. migrationを2回実行し、2回目no-changeを確認する。
6. final backend imageとOrganization frontend SHAを組み合わせて起動する。
7. health、login、level role read/write、policy、profile hideを確認する。
8. gate成功後だけ切替える。

途中で失敗した場合は新環境を破棄し、source側を再開する。切替完了後のCherryPick rollbackは保証しない。

## Test strategy

### Backend

- migration fresh/imported/no-op/shape mismatch
- model/repository round-trip
- const・linear・exponential golden cases
- policy境界・最大level・overflow・invalid input
- set/add/multiplier・assignForce
- concurrent update
- cache invalidation
- API permission/error/shape
- profile hide visibility
- manual/conditional role non-regression

### Frontend

- generated typecheck
- level editor payload
- policy editor conditional rendering
- experience operation dialogs
- role preview・profile hide rendering
- anonymous/self/moderator visibility
- `vue-tsc`、eslint、frontend unit tests

### Integration

- CherryPick schema fixtureからのmigration
- imported DBで既存level roleを読めること
- role作成、experience変更、level更新、policy反映
- member experience順
- profile badge hide/show
- Docker health・frontend boot
- production由来ID、値、件数、path、hashをevidenceへ出さないprivacy gate

## 完了条件

- CherryPick由来level role schemaが無変換で読み取れる。
- fresh DBとimport済みDBでmigrationが成功し、2回目がno-opになる。
- CherryPickのlevel計算・policy・API error semanticsと一致する。
- adminとuserの全操作がOrganization frontendから利用できる。
- profile role hideが本人・anonymous・moderatorの各視点で正しく動く。
- frontend repositoryが`Misaki-Project/misskey-ts`で管理される。
- 6 PRが依存順にreviewされ、最終Docker E2Eが成功する。
- source dump、credential、production由来値がrepository、PR、reportへ入らない。
