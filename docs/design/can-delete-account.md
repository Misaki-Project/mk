# canDeleteAccount互換

## 目的

CherryPickの`canDeleteAccount` role policyをmk-goへ移植し、本人によるaccount削除を実効policyで制御する。既定値は`true`とし、既存instanceの動作を変えない。管理者による他accountの削除は対象外とする。

現行plugin APIだけではnative endpointの認可やnative frontend要素の表示を差し替えられないため、適用箇所はnativeへ最小実装する。一方、policy値の決定は既存のeffective-policy provider境界を通し、将来pluginへ移せる構造を維持する。

## 契約

- native policy catalogへbooleanの`canDeleteAccount`を追加し、既定値を`true`にする。
- `POST /api/i/delete-account`は、認証済み本人の実効`canDeleteAccount`が`true`のときだけ処理する。
- `false`、欠損、不正な型はfail closedとし、`403 ROLE_PERMISSION_DENIED`を返す。
- policy判定は2FA tokenやpasswordの検証より前に行い、拒否されたrequestでは認証情報を消費しない。
- 管理者・rootによる本人削除も実効policyに従う。管理者を常時許可する`HasRolePolicy`は、このendpointでは使用しない。
- `POST /api/admin/delete-account`など管理者による他accountの削除経路は変更しない。
- root・system accountの既存削除防止、2FA、password検証、logical deletion、token invalidation、ActivityPub Delete配信、cascade jobは変更しない。
- `canPurgeAccount`と`canTruncateAccount`は追加しない。

## Backend設計

`internal/effectivepolicy`のnative defaultsへ`canDeleteAccount: true`を追加する。これにより、meta default policy、role override、`/api/i`の`policies`、policy値の型検証、およびplugin effective-policy providerの登録検証が同じschemaを共有する。JSONB内のrole/default policyを使うためDB migrationは不要である。

`internal/core/role`にはpolicy名の定数を追加し、文字列literalをconsumerへ分散させない。

本人削除handlerはproductionのrole serviceが既に実装している`GetUserPoliciesChecked(userID)`から実効値と解決errorを読む。既存の`GetUserPolicies`はrole入力、instance base policy (`meta.policies`)、plugin providerの失敗時にnative policyへfallbackしてerrorを捨てるため、security-sensitiveな削除認可には使用しない。`HasRolePolicy`も管理者を無条件で許可するため、CherryPickの`getUserPolicies(me.id).canDeleteAccount`判定とは一致せず使用しない。

`meta.policies`の読み損ねを握り潰さないのは、他の失敗とfallbackの向きが逆になるためである。運営者がbaseで`canDeleteAccount=false`を指定しているinstanceで、metaのfetch失敗またはJSON decode失敗が重なると、握り潰した側の解決はnative既定値`true`(許可)を返し、**運営者の拒否だけを窓なく失う**。そのため`applyMetaBasePolicies`は失敗をerrorとして返し、`GetUserPoliciesChecked`がそれを渡す。checked経路が受け取る返却map自体は従来と同じ形(native既定 + role override)を保つので、`GetUserPolicies`経由のconsumerはbase障害の窓でもroleによる拒否を失わない。

`RoleProvider`全体へmethodを追加すると無関係なconsumerとtest stubへ変更が広がるため、本人削除が必要とする`GetUserPoliciesChecked`だけのnarrow interfaceを定義し、配線済みproviderへtype assertionする。productionでchecked providerが未配線の場合、role入力解決が失敗した場合、instance base policyを読めなかった場合、またはplugin providerがtimeout・panic・error・invalid outputになった場合は削除を許可せず、server errorとしてfail closedにする。

判定順序は次のとおりとする。

1. 認証済みuserを取得する。
2. checked providerから実効policyを取得し、解決errorが無いことと`canDeleteAccount`がboolean `true`であることを確認する。
3. requestをbindし、既存の2FA・password・保護account判定を行う。
4. 既存の削除処理を実行する。

## Plugin移行境界

native側が恒久的に保持する責務は、公開policy keyのschema、security-sensitiveなendpointでの適用、およびfrontendでの表示条件に限定する。policy値を決定する責務はnative roleだけに固定しない。

`canDeleteAccount`がnative catalogへ登録されると、pluginは既存の`plugin.Definition.EffectivePolicies`で同keyを宣言し、`EffectivePolicyResolver`からcontributionを返せる。handlerはpluginの存在を知らず、native roleとplugin contributionを集約した最終値だけを読む。checked解決はresolver障害をerrorとして返すため、障害時に既定値`true`へfail openしない。したがって将来のplugin化では、endpoint handlerやfrontendを変更せずにpolicy決定ロジックをpluginへ移せる。

**plugin contributionは通常のpriority集約に参加する参加者にすぎない。pluginは拒否を完全には所有できず、その`false`も常に優先もしない。** `EffectivePolicyResolver`が返すcontributionはnative role overrideと同じ`{priority, useDefault, value}`の形で、型検証後に同じpriority cascadeへ入る。boolean keyは同じpriority群のORで集約されるため、**同priorityのロールが`true`を持てばpluginの`false`は打ち消される**。`internal/core/role`の`TestEffectivePolicy_EqualPriorityRoleTrueOverridesPluginDeny`が、plugin priority 2 falseとrole priority 2 trueの衝突で結果が`true`になることを固定する。plugin単独でpriority 2の`false`を返す場合は`TestEffectivePolicy_CanDeleteAccountProviderCanDeny`のとおり`false`になるが、それは「pluginのcontributionが最高priority群で唯一の値だから」であって、pluginに優先権があるためではない。

**pluginのveto (contributionに優先権を与える仕組み) は将来も別の明示的拡張として必要であり、今は何も提供しない。** 上記のとおり現在の集約は通常の型・priority・OR semanticsのままで、特権的な集約規則も専用のplugin APIも存在しない。plugin由来の`false`を必ず確定させたい場合は、priorityの特別扱いまたは集約規則の変更というhost側契約の追加が別途必要になり、それは今回の範囲外である。文書や実装が「pluginが拒否を所有する」と読ませないよう、この境界を明示する。

今回、endpoint middleware差し替えや専用`SelfDeleteAuthorizer`のような新規plugin APIは追加しない。現在の1機能だけを理由にsecurity-sensitiveな汎用interception APIを公開すると、hook順序、plugin未導入時の挙動、障害時fallbackを恒久契約にする必要があり、将来移行を容易にする以上の複雑性を生むためである。

将来pluginがpolicy決定の主担当になる場合でも、次のhost contractは残す。

- `canDeleteAccount`はbooleanで既定`true`の既知keyである。
- endpointは集約後の実効値だけを参照する。
- contributionは通常のpriority・型・OR semanticsで集約される。pluginに優先権はなく、同じpriorityのロール`true`はpluginの`false`を打ち消す。
- provider failure時は通常consumer向けの返却mapでは既存規則どおりnative値へfallbackするが、本人削除handlerはchecked errorを受けて処理を中断する。
- instance base policy (`meta.policies`) の読み損ねも同様にchecked errorとして扱い、返却mapはnative既定 + role overrideの形を保つ。
- frontendは`/api/i`が返す実効値だけを参照し、値の由来を判別しない。

## Frontend設計

本人向け設定画面は`$i.policies.canDeleteAccount === true`の場合だけaccount削除sectionを表示する。backendが常に最終的な認可を行うため、これはsecurity boundaryではなく、正常にpolicyを解決できた通常時に操作不能なUIを隠すための表示制御である。`/api/i`は既存のunchecked fallback契約を維持するため、provider障害中はsectionが表示されても削除requestが500で拒否される場合がある。

base policy画面とrole editorへboolean項目を追加し、instance既定値とrole overrideを管理できるようにする。mk-go固有policyは既存規約どおり`mkGoRolePolicyKeys`、`mkGoPolicyMetaKeys`、`mkGoPolicyValue`へ集約し、TS backendのOpenAPIから再生成される型を直接編集しない。本人設定画面での型境界も小さなboolean判定helperへ閉じ込め、templateへ任意castやprivate plugin API依存を散らさない。

frontendは`third_party/misskey` submoduleで、2026-09-26 時点で submodule URLは`Misaki-Project/misskey-ts`を指す。`shiroha-a/misskey-ts`は上流repositoryとして変更していない。本ドキュメントの作時点で計画していたfork移設は完了し、`canDeleteAccount`のfrontend PR (https://github.com/Misaki-Project/misskey-ts/pull/1) をmergeしたcommit `1a53308b` にtag `2026.9.1-mk.2` を打って、mk-go側はsubmodule URLとgitlinkをそのtagへ更新済み。baseの`mk-2026.9.1`ブランチには別PR (同fork PR #2) でsignup E2Eのtest fixも入っており、どちらも`2026.9.1-mk.2`に含まれる。

bundled deploymentはsubmoduleではなく`Dockerfile.bundled`の`MISSKEY_ASSETS_IMAGE`を使うため、新fork側で同commitのfrontend assets imageをpublishし、mk-go側でimage repository/tagも更新する。submodule pin、assets image tag、`docs/divergence.md`のpin記録、および既存`submodulepin-check`を同じ変更単位で揃え、source buildと配布imageで異なるUIを出さない。公開済みなのは`ghcr.io/misaki-project/misskey-ts-assets:2026.9.1-mk.2` (submodule tagと1:1対応、workflow `Publish frontend assets image` が`*-mk.*`タグで発火)。

将来frontendがmk repositoryへ統合された後も移動しやすいよう、専用frameworkや一時的なDOM操作は追加せず、policy表示条件とrole editor項目だけの最小差分にする。fork固有の変更はfrontend実装とassets publish設定に限定し、mk-go側はsubmodule/assets pin以外からfork repositoryを参照しない。

## Error処理

- policy拒否は既存の`apierr.RolePermissionDenied()`を使い、HTTP 403、code `ROLE_PERMISSION_DENIED`を返す。
- checked provider未配線はserver側の構成不備としてerror logを残し、`500 INTERNAL_ERROR`へ倒す。認可gateをskipしてはならない。
- policy mapにkeyが無い、または値がboolean以外の場合は403へ倒す。
- native role入力の解決失敗、instance base policy (`meta.policies`) のfetch失敗とJSON decode失敗、plugin resolverのtimeout、panic、error、invalid outputはchecked解決のerrorとして扱い、内部情報を返さず`500 INTERNAL_ERROR`へ倒す。本人削除handlerにplugin名や失敗種類ごとの分岐は追加しない。

## テスト

Backendでは次を確認する。

- native defaultが`true`であり、plugin registrationが`canDeleteAccount`を既知keyとして受理する。
- policy `true`では既存の本人削除成功経路が維持される。
- policy `false`ではpasswordが正しくても403となり、user更新、2FA token消費、AP配信、cascade enqueue、session invalidationが発生しない。
- 欠損、不正型、checked provider未配線がfail closedになる。
- native role repositoryの失敗とplugin providerのtimeout・error・invalid outputが500でfail closedになり、削除side effectを起こさない。
- instance base policyのfetch失敗と`meta.policies`のJSON decode失敗が500でfail closedになり、削除side effectを起こさない。productionの`role.Service`を直接配線したhandler testで、base overrideが読めない場合にアカウントが削除されないことを確認する。
- 同じbase policy障害の窓でも`GetUserPolicies`は従来どおりのmap (native既定 + role override)を返し、errorを捨てること。
- administrator/rootでも本人削除はpolicy `false`なら拒否される。
- admin delete endpointの既存testが通り、本人policyの影響を受けない。
- plugin contributionを含む集約後の値が本人削除gateへ反映される。
- plugin contributionは通常の集約参加者であり、同priorityのロール`true`がpluginの`false`を上書きする (plugin veto ではない)。

Frontendでは次を確認する。

- policy `true`では削除sectionが表示され、`false`では表示されない。
- base policy画面とrole editorが`canDeleteAccount`を読み書きできる。
- frontend role-policy key gateが新しいkeyを両方のeditorで検出する。
- local source buildと新forkからpublishしたbundled assets imageの両方に同じ表示制御が含まれ、submodule/assets pin gateが一致する。

## 検証前提

変更前の`go test ./...`は、このworktree環境ではテスト用PostgreSQL role `mk`未構成、plugin公開面goldenの既存drift、およびWindows依存のqueue timing/権限testにより失敗する。実装時は変更対象packageのunit test、frontend test/build、および利用可能なrepository gateを個別に実行し、既存baseline failureと新規regressionを分離する。

## 対象外

- `canPurgeAccount`または`canTruncateAccount`の移植
- 管理者による他account削除の制限
- pluginによるnative routeの置換・middleware挿入API
- frontend pluginからnative設定要素を除去するAPI
- frontendのmk repository統合作業そのもの
