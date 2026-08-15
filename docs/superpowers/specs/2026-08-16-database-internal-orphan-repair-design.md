# DB内部孤児修復設計

## 目的

CherryPick本番dumpの隔離リハーサルで検出された孤児参照を、固有IDをDB session外へ出さずに一回限りで修復する。

修復対象の固有ID、件数、URL、profile値はterminal、file、source、Git履歴、report、evidence、Pull Requestへ出力しない。修復SQLはtracked fileにもrepo外fileにも保存しない。

## 適用範囲

この手順は`cherrypick-rehearsal-prepare`が作成し、migration前で停止している隔離DBだけに適用する。本番DBへ直接実行しない。

実行前に次を確認する。

- 対象containerがリハーサル専用Compose projectのDB serviceである。
- 対象containerが単一のinternal networkだけに接続している。
- 公開portが存在しない。
- 使用imageが既存Composeで固定されたdigestと一致する。
- migrationがまだ実行されていない。

## 修復transaction

修復は1つのPostgreSQL transactionで完結させる。`user`、`drive_file`、`avatar_decoration`を`SHARE ROW EXCLUSIVE MODE`でlockし、検証と更新の間に対象集合が変化しないようにする。

### 事前検証

書込み前に次を検証する。

- 必要tableとcolumnが存在し、query可能である。
- `user.avatarDecorations`の全行がJSON arrayである。
- 配列の全要素がstring型`id`を持つobjectである。
- 空文字hostの装飾が存在しない。
- 孤児banner参照が存在しない。
- 孤児avatar参照を持つ全ユーザーがlocal、削除済み、凍結済みである。
- 孤児avatar参照を持つ全ユーザーが実在するdrive fileを所有していない。

1つでも不一致があれば固定文言のerrorを返し、transactionをrollbackする。errorへ件数や個別値を含めない。

### DB内部snapshot

事前検証後、現在の孤児集合をtemporary tableへsnapshotする。

- 存在しない装飾を参照する装飾ID集合
- 孤児avatar参照のユーザーIDとavatar IDのpair集合

temporary tableはtransaction session内だけに存在し、`ON COMMIT DROP`で破棄する。temporary tableをselectしてclientへ返さない。

### 更新

装飾参照は、temporary tableにsnapshotした孤児装飾IDと一致するJSON要素だけを除去する。残す要素の順序と全fieldを維持し、該当要素がないユーザー行は更新しない。

avatar参照は、temporary tableにsnapshotしたユーザーIDとavatar IDのpairが現在行と一致する場合だけ、次のfieldを`NULL`へ更新する。

- `avatarId`
- `avatarUrl`
- `avatarBlurhash`

bannerや他のprofile fieldは更新しない。

### 事後検証

更新後、次がすべて0件であることをDB内部で確認する。

- 孤児装飾参照
- 孤児avatar参照
- 孤児banner参照

invalid JSONや空文字hostが新たに存在しないことも再確認する。すべて合格した場合だけcommitする。

## 出力制御

generic SQLはcontroller process内の文字列から`psql`標準入力へ直接渡し、fileへ保存しない。

`psql`のstdoutとstderrはcontroller内で捕捉して外部へ出さない。終了codeだけを評価し、ユーザーとreportには次の固定statusだけを返す。

- `repair=passed`
- `repair=failed`

SQLのerror messageも固定categoryだけとし、個別値や件数を含めない。

## Synthetic検証

実データへ適用する前に、同じgeneric SQLを別の一時PostgreSQL containerで検証する。

一時containerは固定digest、`pull_policy: never`相当、internal network、公開portなしで起動し、synthetic schemaとsynthetic IDだけを使う。

最低限、次を検証する。

- validな孤児装飾と安全条件を満たす孤児avatarが修復される。
- local装飾参照、JSON要素順序、追加fieldが維持される。
- unsafe avatar userがある場合は全rollbackする。
- 孤児bannerがある場合は全rollbackする。
- invalid JSONがある場合は全rollbackする。
- SQL error時に全行が実行前と一致する。
- stdout、stderr、reportへsynthetic IDを含めない。

検証後、一時container、volume、networkを完全削除する。

## 実行後フロー

修復成功時だけ`cherrypick-rehearsal-resume`を実行する。resumeは既存の検出専用preflightで全孤児参照0件を再確認してからmigrationへ進む。

修復SQLが失敗した場合は再試行しない。`cherrypick-rehearsal-abort`で隔離container、volume、network、runtimeを完全削除して停止する。

private branchでresumeが成功した後、sanitized public branchをupstream baseから再構成する。public HEADでもfresh volumeからprepare、同じDB内部修復、resumeを再実行する。private branchのevidenceをpublic branchの検証根拠として再利用しない。

## 完了条件

- 本番由来IDがDB session外へ出ていない。
- 修復SQL fileが作成されていない。
- synthetic正常系と全fail-closed系が成功している。
- private HEADとpublic HEADの両方でprepare、修復、resumeが成功している。
- dumpはread-onlyで、local hash比較が前後不変である。
- evidence、report、PRにID、件数、dump情報、path、SQL出力がない。
- 全一時container、volume、network、runtimeが削除されている。
