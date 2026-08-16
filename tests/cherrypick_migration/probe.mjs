// 隔離リハーサルの内部プローブ。
//
// 1. GET /healthz が 2xx なら health=passed を出す。
// 2. 続けて /api/signin を資格情報で叩き、200 かつ finished=true かつ i が
//    存在すれば login=passed を出す。
//
// リクエスト JSON・レスポンス JSON・ユーザー名・パスワード・ユーザー ID・
// トークンを一切出力しない。健康状態とログイン成否だけを標準出力に出し、
// 失敗時は非ゼロで終了する。
//
// MK_PROBE_HEALTH_ONLY=1 のときはログインを省略する (クリーンDBゲート用)。

import { readFile } from 'node:fs/promises';

const baseUrl = process.env.MK_PROBE_BASE_URL ?? 'http://app:3000/';
const healthOnly = process.env.MK_PROBE_HEALTH_ONLY === '1';

async function getJson(url, options) {
  const res = await fetch(url, {
    ...options,
    signal: AbortSignal.timeout(30_000),
  });
  const text = await res.text();
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = null;
  }
  return { status: res.status, body };
}

try {
  const health = await getJson(baseUrl + 'healthz');
  if (health.status < 200 || health.status >= 300) {
    process.exit(1);
  }
  console.log('health=passed');
  if (healthOnly) {
    process.exit(0);
  }

  const credentialText = await readFile('/run/secrets/login.json', 'utf8');
  const credential = JSON.parse(credentialText);

  const login = await getJson(baseUrl + 'api/signin', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      username: credential.username,
      password: credential.password,
    }),
  });
  if (
    login.status !== 200 ||
    login.body === null ||
    login.body.finished !== true ||
    !('i' in login.body)
  ) {
    process.exit(1);
  }
  console.log('login=passed');
} catch {
  process.exit(1);
}
