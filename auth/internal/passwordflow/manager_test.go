package passwordflow

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeCognito struct {
	cognitoAPI
	createInput      *cognitoidentityprovider.AdminCreateUserInput
	setPasswordInput *cognitoidentityprovider.AdminSetUserPasswordInput
	disabled         bool
	enabled          bool
	updated          bool
	deleted          bool
	globalSignOut    bool
	getOutput        *cognitoidentityprovider.AdminGetUserOutput
	getErr           error
	initiateOutput   *cognitoidentityprovider.InitiateAuthOutput
}

func (fake *fakeCognito) AdminCreateUser(_ context.Context, input *cognitoidentityprovider.AdminCreateUserInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminCreateUserOutput, error) {
	fake.createInput = input
	return &cognitoidentityprovider.AdminCreateUserOutput{User: &types.UserType{
		Username:   aws.String("cognito-user"),
		Attributes: []types.AttributeType{{Name: aws.String("sub"), Value: aws.String("user-1")}},
	}}, nil
}

func (fake *fakeCognito) AdminDeleteUser(context.Context, *cognitoidentityprovider.AdminDeleteUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminDeleteUserOutput, error) {
	fake.deleted = true
	return &cognitoidentityprovider.AdminDeleteUserOutput{}, nil
}

func (fake *fakeCognito) AdminSetUserPassword(_ context.Context, input *cognitoidentityprovider.AdminSetUserPasswordInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminSetUserPasswordOutput, error) {
	fake.setPasswordInput = input
	return &cognitoidentityprovider.AdminSetUserPasswordOutput{}, nil
}

func (fake *fakeCognito) AdminDisableUser(context.Context, *cognitoidentityprovider.AdminDisableUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminDisableUserOutput, error) {
	fake.disabled = true
	return &cognitoidentityprovider.AdminDisableUserOutput{}, nil
}

func (fake *fakeCognito) AdminEnableUser(context.Context, *cognitoidentityprovider.AdminEnableUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminEnableUserOutput, error) {
	fake.enabled = true
	return &cognitoidentityprovider.AdminEnableUserOutput{}, nil
}

func (fake *fakeCognito) AdminGetUser(context.Context, *cognitoidentityprovider.AdminGetUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminGetUserOutput, error) {
	return fake.getOutput, fake.getErr
}

func (fake *fakeCognito) AdminUpdateUserAttributes(context.Context, *cognitoidentityprovider.AdminUpdateUserAttributesInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminUpdateUserAttributesOutput, error) {
	fake.updated = true
	return &cognitoidentityprovider.AdminUpdateUserAttributesOutput{}, nil
}

func (fake *fakeCognito) AdminUserGlobalSignOut(context.Context, *cognitoidentityprovider.AdminUserGlobalSignOutInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminUserGlobalSignOutOutput, error) {
	fake.globalSignOut = true
	return &cognitoidentityprovider.AdminUserGlobalSignOutOutput{}, nil
}

func (fake *fakeCognito) InitiateAuth(context.Context, *cognitoidentityprovider.InitiateAuthInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.InitiateAuthOutput, error) {
	return fake.initiateOutput, nil
}

type fakeLifecycle struct {
	lifecycleService
	action             domain.ActionToken
	verificationUserID string
	verificationEmail  string
	verificationErr    error
	resetUserID        string
	resetEmail         string
	completedPurpose   domain.ActionPurpose
	federatedIdentity  application.FederatedIdentity
}

func (fake *fakeLifecycle) RequestEmailVerification(_ context.Context, userID, email string) error {
	fake.verificationUserID = userID
	fake.verificationEmail = email
	return fake.verificationErr
}

func (fake *fakeLifecycle) RequestPasswordReset(_ context.Context, userID, email string) error {
	fake.resetUserID = userID
	fake.resetEmail = email
	return nil
}

func (fake *fakeLifecycle) EmailVerification(context.Context, string) (domain.ActionToken, error) {
	return fake.action, nil
}

func (fake *fakeLifecycle) PasswordReset(context.Context, string) (domain.ActionToken, error) {
	return fake.action, nil
}

func (fake *fakeLifecycle) CompleteEmailVerification(context.Context, string, domain.ActionToken) error {
	fake.completedPurpose = domain.ActionEmailVerification
	return nil
}

func (fake *fakeLifecycle) CompletePasswordReset(context.Context, string, domain.ActionToken) error {
	fake.completedPurpose = domain.ActionPasswordReset
	return nil
}

func (fake *fakeLifecycle) ReleaseEmailVerification(context.Context, string) error {
	return nil
}

func (fake *fakeLifecycle) ReleasePasswordReset(context.Context, string) error {
	return nil
}

func (fake *fakeLifecycle) CompleteLogin(_ context.Context, identity application.FederatedIdentity) (domain.User, bool, error) {
	fake.federatedIdentity = identity
	return domain.User{ID: identity.Subject, Email: identity.Email}, true, nil
}

type fakeIdentityVerifier struct {
	claims authn.IdentityClaims
}

func (fake fakeIdentityVerifier) VerifyIdentityToken(context.Context, string, string) (authn.IdentityClaims, error) {
	return fake.claims, nil
}

func TestSignUpSuppressesCognitoDeliveryAndRequestsAuthDeliveryEvent(t *testing.T) {
	cognito := &fakeCognito{}
	lifecycle := &fakeLifecycle{}
	manager := newTestManager(t, cognito, lifecycle, fakeIdentityVerifier{})

	if err := manager.SignUp(context.Background(), "PERSON@EXAMPLE.COM", "Correct-Horse-7!", "Person"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if cognito.createInput.MessageAction != types.MessageActionTypeSuppress || !cognito.disabled {
		t.Fatalf("Cognito delivery was not suppressed or user was not disabled: %+v", cognito)
	}
	if cognito.setPasswordInput == nil || !cognito.setPasswordInput.Permanent {
		t.Fatal("permanent password was not set in Cognito")
	}
	if lifecycle.verificationUserID != "user-1" || lifecycle.verificationEmail != "person@example.com" {
		t.Fatalf("unexpected verification request: %+v", lifecycle)
	}
}

func TestSignUpDeletesCognitoUserWhenDeliveryTransactionFails(t *testing.T) {
	cognito := &fakeCognito{}
	lifecycle := &fakeLifecycle{verificationErr: errors.New("postgres unavailable")}
	manager := newTestManager(t, cognito, lifecycle, fakeIdentityVerifier{})

	if err := manager.SignUp(context.Background(), "person@example.com", "Correct-Horse-7!", "Person"); err == nil {
		t.Fatal("SignUp() error = nil, want delivery transaction error")
	}
	if !cognito.deleted {
		t.Fatal("Cognito user was not rolled back")
	}
}

func TestConfirmEmailEnablesCognitoUserThenCompletesAuthAction(t *testing.T) {
	cognito := &fakeCognito{}
	lifecycle := &fakeLifecycle{action: domain.ActionToken{UserID: "user-1", Email: "person@example.com"}}
	manager := newTestManager(t, cognito, lifecycle, fakeIdentityVerifier{})

	if err := manager.ConfirmEmail(context.Background(), "verification-token"); err != nil {
		t.Fatalf("ConfirmEmail() error = %v", err)
	}
	if !cognito.updated || !cognito.enabled || lifecycle.completedPurpose != domain.ActionEmailVerification {
		t.Fatalf("verification lifecycle incomplete: cognito=%+v lifecycle=%+v", cognito, lifecycle)
	}
}

func TestLoginPersistsPasswordIdentityAndReturnsTokens(t *testing.T) {
	cognito := &fakeCognito{initiateOutput: &cognitoidentityprovider.InitiateAuthOutput{
		AuthenticationResult: &types.AuthenticationResultType{
			AccessToken: aws.String("access"), RefreshToken: aws.String("refresh"), IdToken: aws.String("identity"), ExpiresIn: 3600,
		},
	}}
	lifecycle := &fakeLifecycle{}
	manager := newTestManager(t, cognito, lifecycle, fakeIdentityVerifier{claims: authn.IdentityClaims{
		Subject: "user-1", Email: "person@example.com", EmailVerified: true, Name: "Person",
	}})

	session, err := manager.Login(context.Background(), "person@example.com", "Correct-Horse-7!")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.AccessToken != "access" || lifecycle.federatedIdentity.Provider != "Password" {
		t.Fatalf("unexpected password session: session=%+v identity=%+v", session, lifecycle.federatedIdentity)
	}
}

func TestResetPasswordRevokesSessionsAndCompletesAuthAction(t *testing.T) {
	cognito := &fakeCognito{}
	lifecycle := &fakeLifecycle{action: domain.ActionToken{UserID: "user-1", Email: "person@example.com"}}
	manager := newTestManager(t, cognito, lifecycle, fakeIdentityVerifier{})

	if err := manager.ResetPassword(context.Background(), "reset-token", "New-Correct-Horse-8!"); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if !cognito.globalSignOut || lifecycle.completedPurpose != domain.ActionPasswordReset {
		t.Fatalf("reset lifecycle incomplete: cognito=%+v lifecycle=%+v", cognito, lifecycle)
	}
}

func newTestManager(t *testing.T, cognito cognitoAPI, lifecycle lifecycleService, verifier identityVerifier) *Manager {
	t.Helper()
	manager, err := New(cognito, lifecycle, verifier, Config{UserPoolID: "pool", ClientID: "client"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return manager
}
