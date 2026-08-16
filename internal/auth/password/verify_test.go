package password

import (
	"testing"

	"github.com/alexedwards/argon2id"
	"golang.org/x/crypto/bcrypt"
)

func TestVerify(t *testing.T) {
	argonHash, err := argon2id.CreateHash("correct", &argon2id.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	bcryptBytes, err := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, plain, hash string
		want              bool
	}{
		{"argon2id correct", "correct", argonHash, true},
		{"argon2id wrong", "wrong", argonHash, false},
		{"bcrypt correct", "correct", string(bcryptBytes), true},
		{"bcrypt wrong", "wrong", string(bcryptBytes), false},
		{"malformed argon2id", "correct", "$argon2id$v=19$m=x,t=3,p=4$bad$bad", false},
		{"unknown argon2 variant", "correct", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA", false},
		{"unknown format", "correct", "plain-text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(tt.plain, tt.hash); got != tt.want {
				t.Fatalf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyAcceptsProductionShapedArgon2(t *testing.T) {
	// 本番dumpが実際に持つパラメータ組。t=256はメモリが小さいが反復が多い正当な組で、
	// 単純なiterations上限では弾かれてしまうため、work-product上限で受け入れる。
	params := []*argon2id.Params{
		{Memory: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32},
		{Memory: 8 * 1024, Iterations: 2, Parallelism: 4, SaltLength: 16, KeyLength: 32},
		{Memory: 512, Iterations: 256, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	}
	for _, p := range params {
		hash, err := argon2id.CreateHash("correct", p)
		if err != nil {
			t.Fatal(err)
		}
		if !Verify("correct", hash) {
			t.Fatalf("Verify() rejected %+v", *p)
		}
		if Verify("wrong", hash) {
			t.Fatalf("Verify() accepted wrong password for %+v", *p)
		}
	}
}

func TestVerifyRejectsExcessiveArgon2Cost(t *testing.T) {
	hash := "$argon2id$v=19$m=4294967295,t=4294967295,p=255$c2FsdA$aGFzaA"
	if Verify("password", hash) {
		t.Fatal("excessive Argon2 parameters must fail closed")
	}
}

func TestVerifyRejectsWorkLimitOnlyViolation(t *testing.T) {
	// メモリ単体は上限内 (65536 KiB <= 262144) だが、memory*iterations のwork積
	// (65536*8 = 524288) が上限を超える。memory上限だけでは弾けない組で、
	// work-product上限単独の拒否を確認する。
	hash := "$argon2id$v=19$m=65536,t=8,p=4$c2FsdA$aGFzaA"
	if Verify("password", hash) {
		t.Fatal("work-limit-only violation must fail closed")
	}
}

func TestVerifyRejectsUint32WorkWraparound(t *testing.T) {
	// m=65536,t=65536 は積が 2^32 になり uint32 では 0 に巻き戻る。
	// uint64 へキャストしないと 0 > maxArgon2TotalWork が偽となり work上限を
	// すり抜けてしまう。積は必ず uint64 で計算すること。
	hash := "$argon2id$v=19$m=65536,t=65536,p=4$c2FsdA$aGFzaA"
	if Verify("password", hash) {
		t.Fatal("uint32 work-product wraparound must fail closed")
	}
}
