package store

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/identity"
)

func TestSetupAdminConsumesBootstrapTokenAndRejectsExpiredOrReplayedTokens(t *testing.T) {
	now := time.Now().UTC()
	service := NewMemoryForSetup("development", "bootstrap-token-with-enough-entropy", now.Add(10*time.Minute), "agent-token", time.Millisecond)
	defer func() { _ = service.Close() }()

	status := service.SetupStatus()
	if !status.Required || status.BootstrapExpiresAt == nil {
		t.Fatalf("initial setup status = %+v, want setup required with an expiry", status)
	}

	_, err := service.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "wrong-bootstrap-token",
		Email:          "owner@example.test",
		DisplayName:    "Owner",
		Password:       "correct horse battery staple",
	})
	requireProblemCode(t, err, "BOOTSTRAP_TOKEN_INVALID")

	admin, err := service.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-with-enough-entropy",
		Email:          "Owner@Example.Test",
		DisplayName:    "Owner",
		Password:       "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("SetupAdmin returned error: %v", err)
	}
	if admin.Email != "owner@example.test" || len(admin.Roles) != 1 || admin.Roles[0] != "platform_admin" {
		t.Fatalf("setup administrator = %+v", admin)
	}
	if service.SetupStatus().Required {
		t.Fatal("setup remained open after the first administrator was created")
	}
	if _, _, err := service.Login("owner@example.test", "correct horse battery staple"); err != nil {
		t.Fatalf("new administrator could not log in: %v", err)
	}
	_, err = service.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-with-enough-entropy",
		Email:          "second@example.test",
		DisplayName:    "Second",
		Password:       "another secure password",
	})
	requireProblemCode(t, err, "SETUP_ALREADY_COMPLETE")

	expired := NewMemoryForSetup("development", "expired-bootstrap-token-value", now.Add(-time.Second), "agent-token", time.Millisecond)
	defer func() { _ = expired.Close() }()
	_, err = expired.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "expired-bootstrap-token-value",
		Email:          "owner@example.test",
		DisplayName:    "Owner",
		Password:       "correct horse battery staple",
	})
	requireProblemCode(t, err, "BOOTSTRAP_TOKEN_INVALID")
}

func TestSetupAndResetRejectInvalidStateBeforeArgon2(t *testing.T) {
	originalHashPassword := passwordHashFunc
	defer func() { passwordHashFunc = originalHashPassword }()
	var calls int
	passwordHashFunc = func(_ string, _ identity.Argon2idParams) (string, error) {
		calls++
		return "stub-password-hash", nil
	}

	setup := NewMemoryForSetup("development", "bootstrap-token-with-enough-entropy", time.Now().Add(10*time.Minute), "agent-token", time.Millisecond)
	defer func() { _ = setup.Close() }()
	if _, err := setup.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "wrong-bootstrap-token", Email: "owner@example.test", DisplayName: "Owner", Password: "correct horse battery staple",
	}); err == nil {
		t.Fatal("invalid bootstrap token was accepted")
	} else {
		requireProblemCode(t, err, "BOOTSTRAP_TOKEN_INVALID")
	}
	if calls != 0 {
		t.Fatalf("invalid bootstrap token entered Argon2 %d times", calls)
	}

	expiredSetup := NewMemoryForSetup("development", "expired-bootstrap-token-value", time.Now().Add(-time.Second), "agent-token", time.Millisecond)
	defer func() { _ = expiredSetup.Close() }()
	if _, err := expiredSetup.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "expired-bootstrap-token-value", Email: "owner@example.test", DisplayName: "Owner", Password: "correct horse battery staple",
	}); err == nil {
		t.Fatal("expired bootstrap token was accepted")
	} else {
		requireProblemCode(t, err, "BOOTSTRAP_TOKEN_INVALID")
	}
	if calls != 0 {
		t.Fatalf("expired bootstrap token entered Argon2 %d times", calls)
	}

	completedSetup := NewMemoryForSetup("development", "bootstrap-token-with-enough-entropy", time.Now().Add(10*time.Minute), "agent-token", time.Millisecond)
	defer func() { _ = completedSetup.Close() }()
	if _, err := completedSetup.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-with-enough-entropy", Email: "owner@example.test", DisplayName: "Owner", Password: "correct horse battery staple",
	}); err != nil {
		t.Fatalf("initial setup failed: %v", err)
	}
	calls = 0
	if _, err := completedSetup.SetupAdmin(domain.SetupAdminInput{
		BootstrapToken: "bootstrap-token-with-enough-entropy", Email: "second@example.test", DisplayName: "Second", Password: "another secure password",
	}); err == nil {
		t.Fatal("setup after completion was accepted")
	} else {
		requireProblemCode(t, err, "SETUP_ALREADY_COMPLETE")
	}
	if calls != 0 {
		t.Fatalf("completed setup entered Argon2 %d times", calls)
	}

	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")
	user, err := service.CreateUser(domain.CreateUserInput{
		Email: "reset-precheck@example.test", DisplayName: "Reset Precheck", Password: "old secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.IssuePasswordResetToken(user.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	digest := tokenDigest(issued.Token)
	record := service.passwordResetTokens[digest]
	record.ExpiresAt = time.Now().Add(-time.Second)
	service.passwordResetTokens[digest] = record
	calls = 0
	if err := service.ResetPassword(issued.Token, "new secure password"); err == nil {
		t.Fatal("expired reset token was accepted")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_RESET_TOKEN")
	}
	if calls != 0 {
		t.Fatalf("expired reset token entered Argon2 %d times", calls)
	}

	activeToken, err := service.IssuePasswordResetToken(user.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(activeToken.Token, "new secure password"); err != nil {
		t.Fatalf("active reset failed: %v", err)
	}
	calls = 0
	if err := service.ResetPassword(activeToken.Token, "replayed secure password"); err == nil {
		t.Fatal("consumed reset token was accepted")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_RESET_TOKEN")
	}
	if calls != 0 {
		t.Fatalf("consumed reset token entered Argon2 %d times", calls)
	}
}

func TestResetPasswordConcurrentRequestsConsumeTokenOnceAfterHashing(t *testing.T) {
	originalHashPassword := passwordHashFunc
	defer func() { passwordHashFunc = originalHashPassword }()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	passwordHashFunc = func(_ string, _ identity.Argon2idParams) (string, error) {
		entered <- struct{}{}
		<-release
		return "stub-password-hash", nil
	}

	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")
	user, err := service.CreateUser(domain.CreateUserInput{
		Email: "reset-race@example.test", DisplayName: "Reset Race", Password: "old secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.IssuePasswordResetToken(user.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	for range 2 {
		go func() {
			defer waitGroup.Done()
			results <- service.ResetPassword(issued.Token, "raced secure password")
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("concurrent reset did not reach the password hash barrier")
		}
	}
	close(release)
	waitGroup.Wait()
	close(results)
	var successes, invalid int
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		requireProblemCode(t, result, "AUTH_INVALID_RESET_TOKEN")
		invalid++
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent reset results = successes:%d invalid:%d, want one each", successes, invalid)
	}
}

func TestUserLifecycleNormalizesEmailAndDisabledUsersLoseSessions(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")

	created, err := service.CreateUser(domain.CreateUserInput{
		Email: "Player@Example.Test", DisplayName: "Player One", Password: "initial secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatalf("CreateUser returned error: %v", err)
	}
	if created.Email != "player@example.test" || created.Status != "active" {
		t.Fatalf("created user = %+v", created)
	}
	if _, err := service.CreateUser(domain.CreateUserInput{
		Email: " player@example.test ", DisplayName: "Duplicate", Password: "another secure password", Roles: []string{"server_owner"},
	}, admin); err == nil {
		t.Fatal("normalized duplicate email was accepted")
	} else {
		requireProblemCode(t, err, "EMAIL_CONFLICT")
	}

	_, token, err := service.Login("PLAYER@example.test", "initial secure password")
	if err != nil {
		t.Fatalf("created user could not log in: %v", err)
	}
	disabled := "disabled"
	updated, err := service.UpdateUser(created.ID, domain.UpdateUserInput{Status: &disabled}, admin)
	if err != nil {
		t.Fatalf("UpdateUser returned error: %v", err)
	}
	if updated.Status != "disabled" {
		t.Fatalf("updated status = %q", updated.Status)
	}
	if _, err := service.Session(token); err == nil {
		t.Fatal("disabled user's existing session remained valid")
	}
	if _, _, err := service.Login("player@example.test", "initial secure password"); err == nil {
		t.Fatal("disabled user could still log in")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_CREDENTIALS")
	}
}

func TestPasswordResetTokenIsSingleUseAndRevokesEveryOldSession(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")
	created, err := service.CreateUser(domain.CreateUserInput{
		Email: "reset@example.test", DisplayName: "Reset User", Password: "old secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	_, firstSession, err := service.Login(created.Email, "old secure password")
	if err != nil {
		t.Fatal(err)
	}
	_, secondSession, err := service.Login(created.Email, "old secure password")
	if err != nil {
		t.Fatal(err)
	}

	issued, err := service.IssuePasswordResetToken(created.ID, admin)
	if err != nil {
		t.Fatalf("IssuePasswordResetToken returned error: %v", err)
	}
	if issued.Token == "" || len(service.passwordResetTokens) != 1 {
		t.Fatalf("issued token or stored records are invalid: issued=%+v records=%d", issued, len(service.passwordResetTokens))
	}
	for digest := range service.passwordResetTokens {
		if string(digest[:]) == issued.Token {
			t.Fatal("password reset token was stored in plaintext")
		}
	}

	if err := service.ResetPassword(issued.Token, "new secure password"); err != nil {
		t.Fatalf("ResetPassword returned error: %v", err)
	}
	for _, token := range []string{firstSession, secondSession} {
		if _, err := service.Session(token); err == nil {
			t.Fatal("password reset left an old session valid")
		}
	}
	if _, _, err := service.Login(created.Email, "old secure password"); err == nil {
		t.Fatal("old password remained valid after reset")
	}
	if _, _, err := service.Login(created.Email, "new secure password"); err != nil {
		t.Fatalf("new password was not accepted: %v", err)
	}
	if err := service.ResetPassword(issued.Token, "third secure password"); err == nil {
		t.Fatal("consumed reset token was accepted again")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_RESET_TOKEN")
	}

	expired, err := service.IssuePasswordResetToken(created.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	digest := tokenDigest(expired.Token)
	record := service.passwordResetTokens[digest]
	record.ExpiresAt = time.Now().Add(-time.Second)
	service.passwordResetTokens[digest] = record
	if err := service.ResetPassword(expired.Token, "another secure password"); err == nil {
		t.Fatal("expired reset token was accepted")
	} else {
		requireProblemCode(t, err, "AUTH_INVALID_RESET_TOKEN")
	}
}

func TestIssuePasswordResetTokenRejectsMissingAndDisabledUsersWithoutIssuingToken(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")
	disabledUser, err := service.CreateUser(domain.CreateUserInput{
		Email: "disabled-reset@example.test", DisplayName: "Disabled Reset", Password: "initial secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	disabled := "disabled"
	if _, err := service.UpdateUser(disabledUser.ID, domain.UpdateUserInput{Status: &disabled}, admin); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		userID   string
		wantCode string
	}{
		{name: "missing user", userID: "ffffffff-ffff-4fff-8fff-ffffffffffff", wantCode: "NOT_FOUND"},
		{name: "disabled user", userID: disabledUser.ID, wantCode: "OPERATION_CONFLICT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokenCount := len(service.passwordResetTokens)
			auditCount := len(service.AuditEvents())
			issued, err := service.IssuePasswordResetToken(test.userID, admin)
			requireProblemCode(t, err, test.wantCode)
			if issued.Token != "" || !issued.ExpiresAt.IsZero() {
				t.Fatalf("failed issue returned a usable token: %+v", issued)
			}
			if got := len(service.passwordResetTokens); got != tokenCount {
				t.Fatalf("failed issue changed stored token count from %d to %d", tokenCount, got)
			}
			events := service.AuditEvents()
			if got := len(events); got != auditCount+1 {
				t.Fatalf("failed issue audit count = %d, want %d", got, auditCount+1)
			}
			if event := events[0]; event.Action != "auth.password_reset.issue" || event.TargetName != test.userID || event.Result != "failure" {
				t.Fatalf("failed issue audit = %+v", event)
			}
		})
	}
}

func TestMembershipAuthorizationFiltersServersAndRevokesImmediately(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin := testActor("00000000-0000-4000-8000-000000000001", "GuGu Admin")
	member, err := service.CreateUser(domain.CreateUserInput{
		Email: "member@example.test", DisplayName: "Member", Password: "member secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.PutServerMembership(stoppedServerID, member.ID, []string{"servers.read", "servers.files.read"}, admin); err != nil {
		t.Fatalf("PutServerMembership returned error: %v", err)
	}
	if err := service.AuthorizeServer(member.ID, stoppedServerID, "servers.read"); err != nil {
		t.Fatalf("read permission was denied: %v", err)
	}
	if err := service.AuthorizeServer(member.ID, stoppedServerID, "servers.power"); err == nil {
		t.Fatal("ungranted power permission was accepted")
	} else {
		requireProblemCode(t, err, "FORBIDDEN")
	}
	visible := service.VisibleServers(member.ID, "")
	if len(visible) != 1 || visible[0].ID != stoppedServerID {
		t.Fatalf("visible servers = %+v, want only %s", visible, stoppedServerID)
	}

	if err := service.DeleteServerMembership(stoppedServerID, member.ID, admin); err != nil {
		t.Fatalf("DeleteServerMembership returned error: %v", err)
	}
	if err := service.AuthorizeServer(member.ID, stoppedServerID, "servers.read"); err == nil {
		t.Fatal("membership deletion did not revoke access immediately")
	} else {
		requireProblemCode(t, err, "NOT_FOUND")
	}
	if visible := service.VisibleServers(member.ID, ""); len(visible) != 0 {
		t.Fatalf("revoked member still sees servers: %+v", visible)
	}
}

func TestEffectiveServerPermissionsReturnsScopedSnapshotAndRevokesImmediately(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, err := service.UserByID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.CreateUser(domain.CreateUserInput{
		Email: "effective-permissions@example.test", DisplayName: "Effective Permissions", Password: "member secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	granted := []string{"servers.read", "servers.files.read", "servers.network.write"}
	if _, err := service.PutServerMembership(stoppedServerID, member.ID, granted, admin); err != nil {
		t.Fatalf("PutServerMembership returned error: %v", err)
	}

	got, err := service.EffectiveServerPermissions(member.ID, stoppedServerID)
	if err != nil {
		t.Fatalf("member permission lookup returned error: %v", err)
	}
	want := []string{"servers.files.read", "servers.network.write", "servers.read"}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Fatalf("member permissions = %#v, want %#v", got.Permissions, want)
	}
	if got.ServerID != stoppedServerID {
		t.Fatalf("permission snapshot server id = %q, want %q", got.ServerID, stoppedServerID)
	}
	got.Permissions[0] = "mutated"
	again, err := service.EffectiveServerPermissions(member.ID, stoppedServerID)
	if err != nil {
		t.Fatalf("second member permission lookup returned error: %v", err)
	}
	if !reflect.DeepEqual(again.Permissions, want) {
		t.Fatalf("permission lookup leaked mutable state = %#v, want %#v", again.Permissions, want)
	}

	adminPermissions, err := service.EffectiveServerPermissions(admin.ID, stoppedServerID)
	if err != nil {
		t.Fatalf("admin permission lookup returned error: %v", err)
	}
	wantAdmin := []string{
		"servers.backups.create", "servers.backups.delete", "servers.backups.read", "servers.backups.restore",
		"servers.console", "servers.files.read", "servers.files.write", "servers.network.read", "servers.network.write",
		"servers.power", "servers.read", "servers.startup.read", "servers.startup.write",
	}
	if !reflect.DeepEqual(adminPermissions.Permissions, wantAdmin) {
		t.Fatalf("admin permissions = %#v, want %#v", adminPermissions.Permissions, wantAdmin)
	}

	if err := service.DeleteServerMembership(stoppedServerID, member.ID, admin); err != nil {
		t.Fatalf("DeleteServerMembership returned error: %v", err)
	}
	if _, err := service.EffectiveServerPermissions(member.ID, stoppedServerID); err == nil {
		t.Fatal("revoked member permission lookup succeeded")
	} else {
		requireProblemCode(t, err, "NOT_FOUND")
	}
}

func TestEffectiveServerPermissionsHidesUnknownServer(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, err := service.UserByID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EffectiveServerPermissions(admin.ID, "ffffffff-ffff-4fff-8fff-ffffffffffff"); err == nil {
		t.Fatal("unknown server permission lookup succeeded")
	} else {
		requireProblemCode(t, err, "NOT_FOUND")
	}
}

func TestEffectiveServerPermissionsHidesMembershipWithoutReadAndRejectsInactiveUsers(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, err := service.UserByID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.CreateUser(domain.CreateUserInput{
		Email: "effective-permissions-inactive@example.test", DisplayName: "Effective Permissions Inactive", Password: "member secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	service.memberships[stoppedServerID] = map[string]domain.ServerMembership{
		member.ID: {ServerID: stoppedServerID, UserID: member.ID, Permissions: []string{"servers.files.read"}},
	}
	service.mu.Unlock()
	if _, err := service.EffectiveServerPermissions(member.ID, stoppedServerID); err == nil {
		t.Fatal("membership without servers.read permission was visible")
	} else {
		requireProblemCode(t, err, "NOT_FOUND")
	}

	disabled := "disabled"
	if _, err := service.UpdateUser(member.ID, domain.UpdateUserInput{Status: &disabled}, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EffectiveServerPermissions(member.ID, stoppedServerID); err == nil {
		t.Fatal("inactive user permission lookup succeeded")
	} else {
		requireProblemCode(t, err, "AUTH_REQUIRED")
	}
}

func TestIdentityAdministrationPreservesLastAdminAndRejectsUnauthorizedActors(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, err := service.UserByID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}

	renamed := "Renamed Administrator"
	updated, err := service.UpdateUser(admin.ID, domain.UpdateUserInput{DisplayName: &renamed}, admin)
	if err != nil {
		t.Fatalf("renaming the last administrator failed: %v", err)
	}
	if updated.DisplayName != renamed {
		t.Fatalf("updated display name = %q", updated.DisplayName)
	}

	roles := []string{"server_owner"}
	if _, err := service.UpdateUser(admin.ID, domain.UpdateUserInput{Roles: &roles}, admin); err == nil {
		t.Fatal("last active platform administrator was demoted")
	} else {
		requireProblemCode(t, err, "OPERATION_CONFLICT")
	}

	unauthorized := domain.User{ID: "not-an-admin", DisplayName: "Untrusted", Roles: []string{"platform_admin"}}
	if _, err := service.CreateUser(domain.CreateUserInput{
		Email: "bypass@example.test", DisplayName: "Bypass", Password: "secure bypass password", Roles: []string{"server_owner"},
	}, unauthorized); err == nil {
		t.Fatal("an actor absent from the store created a user")
	} else {
		requireProblemCode(t, err, "FORBIDDEN")
	}
}

func TestMembershipRequiresBaseReadPermission(t *testing.T) {
	service := newTestMemory(time.Millisecond)
	defer func() { _ = service.Close() }()
	admin, err := service.UserByID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.CreateUser(domain.CreateUserInput{
		Email: "permission@example.test", DisplayName: "Permission User", Password: "permission secure password", Roles: []string{"server_owner"},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutServerMembership(stoppedServerID, member.ID, []string{"servers.power"}, admin); err == nil {
		t.Fatal("membership without servers.read was accepted")
	} else {
		requireProblemCode(t, err, "VALIDATION_FAILED")
	}
}
