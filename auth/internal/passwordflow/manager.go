package passwordflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type cognitoAPI interface {
	AdminCreateUser(context.Context, *cognitoidentityprovider.AdminCreateUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminCreateUserOutput, error)
	AdminDeleteUser(context.Context, *cognitoidentityprovider.AdminDeleteUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminDeleteUserOutput, error)
	AdminSetUserPassword(context.Context, *cognitoidentityprovider.AdminSetUserPasswordInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminSetUserPasswordOutput, error)
	AdminDisableUser(context.Context, *cognitoidentityprovider.AdminDisableUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminDisableUserOutput, error)
	AdminEnableUser(context.Context, *cognitoidentityprovider.AdminEnableUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminEnableUserOutput, error)
	AdminGetUser(context.Context, *cognitoidentityprovider.AdminGetUserInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminGetUserOutput, error)
	AdminUpdateUserAttributes(context.Context, *cognitoidentityprovider.AdminUpdateUserAttributesInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminUpdateUserAttributesOutput, error)
	AdminUserGlobalSignOut(context.Context, *cognitoidentityprovider.AdminUserGlobalSignOutInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.AdminUserGlobalSignOutOutput, error)
	InitiateAuth(context.Context, *cognitoidentityprovider.InitiateAuthInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.InitiateAuthOutput, error)
	GetTokensFromRefreshToken(context.Context, *cognitoidentityprovider.GetTokensFromRefreshTokenInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.GetTokensFromRefreshTokenOutput, error)
}

type lifecycleService interface {
	RequestEmailVerification(context.Context, string, string) error
	RequestPasswordReset(context.Context, string, string) error
	EmailVerification(context.Context, string) (domain.ActionToken, error)
	PasswordReset(context.Context, string) (domain.ActionToken, error)
	CompleteEmailVerification(context.Context, string, domain.ActionToken) error
	CompletePasswordReset(context.Context, string, domain.ActionToken) error
	ReleaseEmailVerification(context.Context, string) error
	ReleasePasswordReset(context.Context, string) error
	CompleteLogin(context.Context, application.FederatedIdentity) (domain.User, bool, error)
}

type identityVerifier interface {
	VerifyIdentityToken(context.Context, string, string) (authn.IdentityClaims, error)
}

type Config struct {
	UserPoolID string
	ClientID   string
}

type Manager struct {
	client   cognitoAPI
	service  lifecycleService
	verifier identityVerifier
	config   Config
}

type Session struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	User         domain.User
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Refresh renews a session through the user-pools API, which takes the
// refresh token of every session the web app client issues: a password
// sign-in's and a Google sign-in's through managed login alike. It is the
// call Cognito names for app clients with refresh-token rotation, as this
// one has; REFRESH_TOKEN_AUTH is refused there.
func (manager *Manager) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, domain.ErrUnauthorized
	}
	output, err := manager.client.GetTokensFromRefreshToken(ctx, &cognitoidentityprovider.GetTokensFromRefreshTokenInput{
		ClientId:     aws.String(manager.config.ClientID),
		RefreshToken: aws.String(refreshToken),
	})
	if err != nil {
		return Tokens{}, mapCognitoError("refresh session", err)
	}
	if output == nil || output.AuthenticationResult == nil || aws.ToString(output.AuthenticationResult.AccessToken) == "" || output.AuthenticationResult.ExpiresIn <= 0 {
		return Tokens{}, domain.ErrUnauthorized
	}
	result := output.AuthenticationResult
	newRefreshToken := aws.ToString(result.RefreshToken)
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}
	return Tokens{AccessToken: aws.ToString(result.AccessToken), RefreshToken: newRefreshToken, ExpiresIn: int(result.ExpiresIn)}, nil
}

func New(client cognitoAPI, service lifecycleService, verifier identityVerifier, config Config) (*Manager, error) {
	if client == nil || service == nil || verifier == nil || config.UserPoolID == "" || config.ClientID == "" {
		return nil, errors.New("Cognito client, lifecycle service, verifier, user pool ID, and client ID are required")
	}
	return &Manager{client: client, service: service, verifier: verifier, config: config}, nil
}

func (manager *Manager) SignUp(ctx context.Context, email, password, displayName string) error {
	email = normalizeEmail(email)
	if !validEmail(email) || password == "" {
		return fmt.Errorf("valid email and password are required: %w", domain.ErrInvalid)
	}
	temporaryPassword, err := randomTemporaryPassword()
	if err != nil {
		return fmt.Errorf("create temporary password: %w", err)
	}
	attributes := []types.AttributeType{
		{Name: aws.String("email"), Value: aws.String(email)},
		{Name: aws.String("email_verified"), Value: aws.String("false")},
	}
	if displayName = strings.TrimSpace(displayName); displayName != "" {
		attributes = append(attributes, types.AttributeType{Name: aws.String("name"), Value: aws.String(displayName)})
	}
	created, err := manager.client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		MessageAction:     types.MessageActionTypeSuppress,
		TemporaryPassword: aws.String(temporaryPassword),
		UserAttributes:    attributes,
		Username:          aws.String(email),
		UserPoolId:        aws.String(manager.config.UserPoolID),
	})
	if err != nil {
		return mapCognitoError("create password user", err)
	}
	rollback := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, rollbackErr := manager.client.AdminDeleteUser(rollbackCtx, &cognitoidentityprovider.AdminDeleteUserInput{
			UserPoolId: aws.String(manager.config.UserPoolID),
			Username:   aws.String(email),
		})
		if rollbackErr != nil {
			return fmt.Errorf("%w; rollback Cognito user: %v", cause, rollbackErr)
		}
		return cause
	}
	if _, err := manager.client.AdminSetUserPassword(ctx, &cognitoidentityprovider.AdminSetUserPasswordInput{
		Password:   aws.String(password),
		Permanent:  true,
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(email),
	}); err != nil {
		return rollback(mapCognitoError("set password", err))
	}
	if _, err := manager.client.AdminDisableUser(ctx, &cognitoidentityprovider.AdminDisableUserInput{
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(email),
	}); err != nil {
		return rollback(mapCognitoError("disable unverified user", err))
	}
	subject := ""
	if created != nil && created.User != nil {
		subject = attribute(created.User.Attributes, "sub")
		if subject == "" {
			subject = aws.ToString(created.User.Username)
		}
	}
	if subject == "" {
		return rollback(errors.New("Cognito created a user without an identifier"))
	}
	if err := manager.service.RequestEmailVerification(ctx, subject, email); err != nil {
		return rollback(fmt.Errorf("request verification delivery: %w", err))
	}
	return nil
}

func (manager *Manager) ResendEmailVerification(ctx context.Context, email string) error {
	user, err := manager.user(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if attribute(user.UserAttributes, "email_verified") == "true" {
		return nil
	}
	return manager.service.RequestEmailVerification(ctx, attributeOrUsername(user), normalizeEmail(email))
}

func (manager *Manager) ConfirmEmail(ctx context.Context, token string) error {
	action, err := manager.service.EmailVerification(ctx, token)
	if err != nil {
		return err
	}
	if _, err := manager.client.AdminUpdateUserAttributes(ctx, &cognitoidentityprovider.AdminUpdateUserAttributesInput{
		UserAttributes: []types.AttributeType{{Name: aws.String("email_verified"), Value: aws.String("true")}},
		UserPoolId:     aws.String(manager.config.UserPoolID),
		Username:       aws.String(action.Email),
	}); err != nil {
		_ = manager.service.ReleaseEmailVerification(ctx, token)
		return mapCognitoError("verify email", err)
	}
	if _, err := manager.client.AdminEnableUser(ctx, &cognitoidentityprovider.AdminEnableUserInput{
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(action.Email),
	}); err != nil {
		_ = manager.service.ReleaseEmailVerification(ctx, token)
		return mapCognitoError("enable verified user", err)
	}
	if err := manager.service.CompleteEmailVerification(ctx, token, action); err != nil {
		_ = manager.service.ReleaseEmailVerification(ctx, token)
		return err
	}
	return nil
}

func (manager *Manager) Login(ctx context.Context, email, password string) (Session, error) {
	email = normalizeEmail(email)
	if !validEmail(email) || password == "" {
		return Session{}, fmt.Errorf("valid email and password are required: %w", domain.ErrInvalid)
	}
	output, err := manager.client.InitiateAuth(ctx, &cognitoidentityprovider.InitiateAuthInput{
		AuthFlow: types.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{
			"USERNAME": email,
			"PASSWORD": password,
		},
		ClientId: aws.String(manager.config.ClientID),
	})
	if err != nil {
		return Session{}, mapCognitoError("password login", err)
	}
	if output == nil || output.AuthenticationResult == nil || output.ChallengeName != "" {
		return Session{}, domain.ErrUnauthorized
	}
	result := output.AuthenticationResult
	if aws.ToString(result.AccessToken) == "" || aws.ToString(result.RefreshToken) == "" || aws.ToString(result.IdToken) == "" || result.ExpiresIn <= 0 {
		return Session{}, domain.ErrUnauthorized
	}
	claims, err := manager.verifier.VerifyIdentityToken(ctx, aws.ToString(result.IdToken), "")
	if err != nil {
		return Session{}, fmt.Errorf("password identity token rejected: %w", domain.ErrUnauthorized)
	}
	user, _, err := manager.service.CompleteLogin(ctx, application.FederatedIdentity{
		Subject:     claims.Subject,
		Email:       claims.Email,
		DisplayName: claims.Name,
		AvatarURL:   claims.Picture,
		Provider:    "Password",
	})
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken:  aws.ToString(result.AccessToken),
		RefreshToken: aws.ToString(result.RefreshToken),
		ExpiresIn:    int(result.ExpiresIn),
		User:         user,
	}, nil
}

func (manager *Manager) RequestPasswordReset(ctx context.Context, email string) error {
	user, err := manager.user(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !user.Enabled || attribute(user.UserAttributes, "email_verified") != "true" {
		return nil
	}
	return manager.service.RequestPasswordReset(ctx, attributeOrUsername(user), normalizeEmail(email))
}

func (manager *Manager) ResetPassword(ctx context.Context, token, password string) error {
	if password == "" {
		return fmt.Errorf("password is required: %w", domain.ErrInvalid)
	}
	action, err := manager.service.PasswordReset(ctx, token)
	if err != nil {
		return err
	}
	if _, err := manager.client.AdminSetUserPassword(ctx, &cognitoidentityprovider.AdminSetUserPasswordInput{
		Password:   aws.String(password),
		Permanent:  true,
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(action.Email),
	}); err != nil {
		_ = manager.service.ReleasePasswordReset(ctx, token)
		return mapCognitoError("reset password", err)
	}
	if _, err := manager.client.AdminUserGlobalSignOut(ctx, &cognitoidentityprovider.AdminUserGlobalSignOutInput{
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(action.Email),
	}); err != nil {
		_ = manager.service.ReleasePasswordReset(ctx, token)
		return mapCognitoError("revoke sessions after password reset", err)
	}
	if err := manager.service.CompletePasswordReset(ctx, token, action); err != nil {
		_ = manager.service.ReleasePasswordReset(ctx, token)
		return err
	}
	return nil
}

func (manager *Manager) user(ctx context.Context, email string) (*cognitoidentityprovider.AdminGetUserOutput, error) {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return nil, fmt.Errorf("valid email is required: %w", domain.ErrInvalid)
	}
	user, err := manager.client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{
		UserPoolId: aws.String(manager.config.UserPoolID),
		Username:   aws.String(email),
	})
	if err != nil {
		var notFound *types.UserNotFoundException
		if errors.As(err, &notFound) {
			return nil, domain.ErrNotFound
		}
		return nil, mapCognitoError("get password user", err)
	}
	return user, nil
}

func mapCognitoError(operation string, err error) error {
	var usernameExists *types.UsernameExistsException
	var aliasExists *types.AliasExistsException
	if errors.As(err, &usernameExists) || errors.As(err, &aliasExists) {
		return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
	}
	var invalidPassword *types.InvalidPasswordException
	var passwordHistory *types.PasswordHistoryPolicyViolationException
	var invalidParameter *types.InvalidParameterException
	if errors.As(err, &invalidPassword) || errors.As(err, &passwordHistory) || errors.As(err, &invalidParameter) {
		return fmt.Errorf("%s: %w", operation, domain.ErrInvalid)
	}
	var notAuthorized *types.NotAuthorizedException
	var userNotFound *types.UserNotFoundException
	var userNotConfirmed *types.UserNotConfirmedException
	if errors.As(err, &notAuthorized) || errors.As(err, &userNotFound) || errors.As(err, &userNotConfirmed) {
		return fmt.Errorf("%s: %w", operation, domain.ErrUnauthorized)
	}
	var limitExceeded *types.LimitExceededException
	var tooManyRequests *types.TooManyRequestsException
	if errors.As(err, &limitExceeded) || errors.As(err, &tooManyRequests) {
		return fmt.Errorf("%s: %w", operation, domain.ErrRateLimited)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func attribute(attributes []types.AttributeType, name string) string {
	for _, item := range attributes {
		if aws.ToString(item.Name) == name {
			return aws.ToString(item.Value)
		}
	}
	return ""
}

func attributeOrUsername(user *cognitoidentityprovider.AdminGetUserOutput) string {
	if subject := attribute(user.UserAttributes, "sub"); subject != "" {
		return subject
	}
	return aws.ToString(user.Username)
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func randomTemporaryPassword() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(value), nil
}
