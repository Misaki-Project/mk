package model

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
)

// RoleTarget defines how a role is assigned to users.
type RoleTarget string

const (
	RoleTargetManual      RoleTarget = "manual"
	RoleTargetConditional RoleTarget = "conditional"
	// RoleTargetManualLevel is a CherryPick level role: the assignment
	// carries an experience value that maps to a level via LevelPolicies.
	RoleTargetManualLevel RoleTarget = "manualLevel"
)

// ExperiencePolicyType enumerates the level experience curve types.
type ExperiencePolicyType string

const (
	ExperiencePolicyConst       ExperiencePolicyType = "const"
	ExperiencePolicyLinear      ExperiencePolicyType = "linear"
	ExperiencePolicyExponential ExperiencePolicyType = "exponential"
)

// ExperiencePolicy is one element of levelPolicies.experiencePolicies.
// Field types match CherryPick model Role.ts: level/base are numbers,
// additional/exponential are optional numbers.
type ExperiencePolicy struct {
	Level       int      `json:"level"`
	Type        string   `json:"type"`
	Base        float64  `json:"base"`
	Additional  *float64 `json:"additional,omitempty"`
	Exponential *float64 `json:"exponential,omitempty"`
}

// LevelPolicies is the parsed shape of role.levelPolicies.
type LevelPolicies struct {
	BaseLevel          int                `json:"baseLevel"`
	ExperiencePolicies []ExperiencePolicy `json:"experiencePolicies"`
}

// ParseLevelPolicies decodes a role.levelPolicies jsonb value into the
// typed shape. The `{baseLevel, experiencePolicies}` keys must both be
// present (raw presence check: a plain struct unmarshal would silently
// default a missing baseLevel to 0). An empty experiencePolicies array is
// valid (a role pinned at baseLevel); every present element must carry an
// interpretable type.
func ParseLevelPolicies(raw []byte) (*LevelPolicies, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("level policies: empty jsonb")
	}
	// 生の key presence を先に検証する。struct への素の Unmarshal では
	// baseLevel 欠落が 0 に黙って丸まるため、`{baseLevel, experiencePolicies}`
	// の両 key の存在を map で確認する。
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(raw, &presence); err != nil {
		return nil, fmt.Errorf("level policies: not a json object: %w", err)
	}
	baseRaw, hasBase := presence["baseLevel"]
	if !hasBase {
		return nil, fmt.Errorf("level policies: missing baseLevel")
	}
	if _, ok := presence["experiencePolicies"]; !ok {
		return nil, fmt.Errorf("level policies: missing experiencePolicies")
	}
	// baseLevel は integer・experiencePolicies は array でなければならない。
	// typed Unmarshal は 1.5 / 文字列 / object を拒否するが、JSON null は
	// int/slice フィールドへ黙って zero / nil に丸めるため、null はここで
	// 先に reject する (baseLevel:null を base level 0 と誤読しない)。
	// experiencePolicies:null は下の nil-array guard でも fail-closed になるが、
	// 一貫性のため baseLevel と同系統の raw-null 検査で受け付ける。
	if string(baseRaw) == "null" {
		return nil, fmt.Errorf("level policies: baseLevel must be a number")
	}
	if string(presence["experiencePolicies"]) == "null" {
		return nil, fmt.Errorf("level policies: experiencePolicies must be an array")
	}
	var lp LevelPolicies
	if err := json.Unmarshal(raw, &lp); err != nil {
		return nil, fmt.Errorf("level policies: invalid shape: %w", err)
	}
	if lp.ExperiencePolicies == nil {
		return nil, fmt.Errorf("level policies: experiencePolicies must be an array")
	}
	for i, p := range lp.ExperiencePolicies {
		switch ExperiencePolicyType(p.Type) {
		case ExperiencePolicyConst, ExperiencePolicyLinear, ExperiencePolicyExponential:
		default:
			return nil, fmt.Errorf("level policies: policy[%d]: unknown type %q", i, p.Type)
		}
	}
	return &lp, nil
}

// Role represents the `role` table.
type Role struct {
	ID              string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UpdatedAt       time.Time      `gorm:"column:updatedAt;not null" json:"updatedAt"`
	LastUsedAt      time.Time      `gorm:"column:lastUsedAt;not null" json:"lastUsedAt"`
	Name            string         `gorm:"column:name;type:varchar(256);not null" json:"name"`
	Description     string         `gorm:"column:description;type:varchar(1024);not null;default:''" json:"description"`
	Color           *string        `gorm:"column:color;type:varchar(256)" json:"color"`
	IconURL         *string        `gorm:"column:iconUrl;type:varchar(512)" json:"iconUrl"`
	Target          RoleTarget     `gorm:"column:target;type:role_target_enum;not null;default:'manual'" json:"target"`
	CondFormula     datatypes.JSON `gorm:"column:condFormula;type:jsonb;default:'{}'" json:"condFormula"`
	IsPublic        bool           `gorm:"column:isPublic;default:false" json:"isPublic"`
	AsBadge         bool           `gorm:"column:asBadge;default:false" json:"asBadge"`
	IsModerator     bool           `gorm:"column:isModerator;default:false" json:"isModerator"`
	IsAdministrator bool           `gorm:"column:isAdministrator;default:false" json:"isAdministrator"`
	IsExplorable    bool           `gorm:"column:isExplorable;default:false" json:"isExplorable"`
	DisplayOrder    int            `gorm:"column:displayOrder;default:0" json:"displayOrder"`
	Policies        datatypes.JSON `gorm:"column:policies;type:jsonb;default:'{}'" json:"policies"`

	// LevelPolicies は manualLevel role の level曲線 (CherryPick role.levelPolicies)。
	LevelPolicies        datatypes.JSON `gorm:"column:levelPolicies;type:jsonb;not null;default:'{}'" json:"levelPolicies"`
	CanHideProfileByUser bool           `gorm:"column:canHideProfileByUser;not null;default:false" json:"canHideProfileByUser"`

	PreserveAssignmentOnMoveAccount bool `gorm:"column:preserveAssignmentOnMoveAccount;default:false" json:"preserveAssignmentOnMoveAccount"`
	CanEditMembersByModerator       bool `gorm:"column:canEditMembersByModerator;default:false" json:"canEditMembersByModerator"`
}

func (Role) TableName() string { return "role" }

// RoleAssignment represents the `role_assignment` table.
type RoleAssignment struct {
	ID        string     `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID    string     `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	RoleID    string     `gorm:"column:roleId;type:varchar(32);not null" json:"roleId"`
	ExpiresAt *time.Time `gorm:"column:expiresAt" json:"expiresAt"`

	// Experience は manualLevel role の累積経験値。DB は bigint。
	// 外部APIで扱う値は 0..Number.MAX_SAFE_INTEGER へ制限する。
	Experience *int64 `gorm:"column:experience" json:"experience"`
	// IsHideProfile は本人が profile badge を隠した状態 (nullable)。
	IsHideProfile *bool `gorm:"column:isHideProfile" json:"isHideProfile"`

	// Relations
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Role *Role `gorm:"foreignKey:RoleID" json:"role,omitempty"`
}

func (RoleAssignment) TableName() string { return "role_assignment" }
