package admin

import "strings"

// Roles of the admin panel (ToR A.11).
const (
	RoleSuper   = "super"
	RoleFinance = "finance"
	RoleSupport = "support"
	RoleContent = "content"
)

var Roles = []string{RoleSuper, RoleFinance, RoleSupport, RoleContent}

func ValidRole(r string) bool {
	for _, x := range Roles {
		if x == r {
			return true
		}
	}
	return false
}

// Permissions are "area.action" strings. A trailing ".*" grants every action of the area; "*" grants everything.
var rolePerms = map[string][]string{
	RoleSuper: {"*"},
	RoleFinance: {
		"dashboard.read", "players.read", "ledger.read", "withdrawals.*", "payments.*", "topup.*", "settings.read",
		"settings.write.withdrawal", "settings.write.topup", "audit.read.own", "payouts.*", "tournaments.read",
	},
	RoleSupport: {
		"dashboard.read", "players.read", "players.block", "players.note", "ledger.read", "gifts.*", "referrals.read",
		"settings.read", "tournaments.read", "audit.read.own",
	},
	RoleContent: {
		"dashboard.read", "tasks.*", "combo.*", "music.*", "content.*", "notifications.*", "tournaments.*", "ads.read",
		"settings.read", "settings.write.links", "settings.write.notifications", "settings.write.daily",
		"settings.write.system", "audit.read.own",
	},
}

// Can reports whether a role grants a permission.
func Can(role, perm string) bool {
	for _, g := range rolePerms[role] {
		if g == "*" || g == perm {
			return true
		}
		if strings.HasSuffix(g, ".*") && strings.HasPrefix(perm, strings.TrimSuffix(g, "*")) {
			return true
		}
	}
	return false
}
