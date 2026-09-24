package resetpassword

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shiroha-a/mk/internal/testutil"
)

func TestHandler_HasMetaRepo(t *testing.T) {
	assert.False(t, (&Handler{}).HasMetaRepo(), "未配線なら false")

	h := &Handler{}
	h.SetMetaRepo(testutil.NewMockMetaRepository())
	assert.True(t, h.HasMetaRepo(), "配線したら true")
}
