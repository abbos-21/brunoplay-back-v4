package admin_test

import (
	"context"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/testutil"
	"brunoplay/internal/token"
)

const goodPW = "Str0ng-Passw0rd!"

func TestPasswordPolicyAndHash(t *testing.T) {
	for _, weak := range []string{"short1A!", "alllowercaseletters", "ALLUPPERCASELETTERS", "123456789012", "Password"} {
		if admin.CheckPassword(weak) == nil {
			t.Errorf("%q must be rejected", weak)
		}
	}
	if err := admin.CheckPassword(goodPW); err != nil {
		t.Fatal(err)
	}
	h, err := admin.HashPassword(goodPW)
	if err != nil {
		t.Fatal(err)
	}
	if !admin.VerifyPassword(goodPW, h) || admin.VerifyPassword(goodPW+"x", h) {
		t.Fatal("verify is wrong")
	}
	h2, _ := admin.HashPassword(goodPW)
	if h == h2 {
		t.Fatal("salts must differ")
	}
}

func TestRBAC(t *testing.T) {
	cases := []struct {
		role, perm string
		want       bool
	}{
		{admin.RoleSuper, "anything.at.all", true},
		{admin.RoleFinance, "withdrawals.approve", true},
		{admin.RoleFinance, "settings.write.withdrawal", true},
		{admin.RoleFinance, "settings.write.games", false},
		{admin.RoleFinance, "admins.manage", false},
		{admin.RoleSupport, "players.block", true},
		{admin.RoleSupport, "withdrawals.read", false},
		{admin.RoleSupport, "gifts.deliver", true},
		{admin.RoleContent, "combo.write", true},
		{admin.RoleContent, "players.block", false},
		{"nobody", "players.read", false},
	}
	for _, c := range cases {
		if got := admin.Can(c.role, c.perm); got != c.want {
			t.Errorf("Can(%s,%s)=%v want %v", c.role, c.perm, got, c.want)
		}
	}
}

func newSvc(t *testing.T) (*admin.Service, context.Context, *time.Time) {
	pool := testutil.DB(t)
	s := admin.NewService(pool, token.New("0123456789abcdef0123456789abcdef", token.Admin))
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	return s, context.Background(), &now
}

func TestLoginLockoutAndRefreshRotation(t *testing.T) {
	s, ctx, now := newSvc(t)
	a, err := s.Create(ctx, admin.CreateInput{Email: "Boss@Example.com", Password: goodPW, Role: admin.RoleSuper})
	if err != nil || a.Email != "boss@example.com" {
		t.Fatalf("create: %v %+v", err, a)
	}
	if _, err := s.Create(ctx, admin.CreateInput{Email: "boss@example.com", Password: goodPW, Role: admin.RoleSuper}); err == nil {
		t.Fatal("duplicate email must be refused")
	}
	if _, err := s.Create(ctx, admin.CreateInput{Email: "w@example.com", Password: "weak", Role: admin.RoleSuper}); err == nil {
		t.Fatal("weak password must be refused")
	}

	// wrong password five times locks the account, even the right password fails while locked
	for i := 0; i < 5; i++ {
		if _, err := s.Login(ctx, "boss@example.com", "wrong", "1.1.1.1", "ua"); !apperr.Is(err, apperr.AuthFailed) {
			t.Fatalf("bad login must be AUTH_FAILED, got %v", err)
		}
	}
	if _, err := s.Login(ctx, "boss@example.com", goodPW, "1.1.1.1", "ua"); !apperr.Is(err, apperr.AuthFailed) {
		t.Fatalf("locked account must refuse the right password, got %v", err)
	}
	*now = now.Add(16 * time.Minute)
	sess, err := s.Login(ctx, "boss@example.com", goodPW, "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("login after lock expired: %v", err)
	}
	if _, err := s.Login(ctx, "nobody@example.com", goodPW, "1.1.1.1", "ua"); !apperr.Is(err, apperr.AuthFailed) {
		t.Fatalf("unknown email must look the same: %v", err)
	}

	who, err := s.Verify(ctx, sess.AccessToken)
	if err != nil || who.Role != admin.RoleSuper {
		t.Fatalf("verify access token: %v %+v", err, who)
	}

	next, err := s.Refresh(ctx, sess.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, sess.RefreshToken); !apperr.Is(err, apperr.AuthExpired) {
		t.Fatalf("a used refresh token must not work twice, got %v", err)
	}
	if _, err := s.Refresh(ctx, next.RefreshToken); err != nil {
		t.Fatalf("the new refresh token must work: %v", err)
	}

	// access token expires after 15 minutes
	*now = now.Add(admin.AccessTTL + time.Minute)
	if _, err := s.Verify(ctx, sess.AccessToken); !apperr.Is(err, apperr.AuthExpired) {
		t.Fatalf("expired access token: %v", err)
	}
}

func TestLastSuperAdminProtected(t *testing.T) {
	s, ctx, _ := newSvc(t)
	a, _ := s.Create(ctx, admin.CreateInput{Email: "a@example.com", Password: goodPW, Role: admin.RoleSuper})
	role := admin.RoleSupport
	if _, err := s.Update(ctx, a.ID, admin.UpdateInput{Role: &role}); err == nil {
		t.Fatal("the last super admin cannot be demoted")
	}
	off := false
	if _, err := s.Update(ctx, a.ID, admin.UpdateInput{IsActive: &off}); err == nil {
		t.Fatal("the last super admin cannot be deactivated")
	}
	b, _ := s.Create(ctx, admin.CreateInput{Email: "b@example.com", Password: goodPW, Role: admin.RoleSuper})
	if _, err := s.Update(ctx, a.ID, admin.UpdateInput{Role: &role}); err != nil {
		t.Fatalf("with a second super admin demotion is fine: %v", err)
	}
	// deactivating signs the admin out everywhere
	sess, _ := s.Login(ctx, "b@example.com", goodPW, "2.2.2.2", "")
	if _, err := s.Update(ctx, b.ID, admin.UpdateInput{IsActive: &off}); err == nil {
		// b is now the last active super admin
		t.Fatal("b is the last super admin")
	}
	_ = sess
}

func TestDeactivatedAdminCannotUseToken(t *testing.T) {
	s, ctx, _ := newSvc(t)
	_, _ = s.Create(ctx, admin.CreateInput{Email: "root@example.com", Password: goodPW, Role: admin.RoleSuper})
	f, _ := s.Create(ctx, admin.CreateInput{Email: "fin@example.com", Password: goodPW, Role: admin.RoleFinance})
	sess, err := s.Login(ctx, "fin@example.com", goodPW, "3.3.3.3", "")
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.Update(ctx, f.ID, admin.UpdateInput{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, sess.AccessToken); err == nil {
		t.Fatal("a deactivated admin's token must stop working")
	}
	if _, err := s.Refresh(ctx, sess.RefreshToken); err == nil {
		t.Fatal("a deactivated admin's refresh token must stop working")
	}
}
