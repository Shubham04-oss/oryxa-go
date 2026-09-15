package awsidentity

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		wantErr bool
	}{
		{name: "default chain", options: Options{}},
		{name: "role", options: Options{RoleARN: "arn:aws:iam::123456789012:role/agents/primary", RoleSessionName: "primary-local"}},
		{name: "role with external ID", options: Options{RoleARN: "arn:aws-us-gov:iam::123456789012:role/primary", RoleSessionName: "primary-local", ExternalID: "tenant/acme:primary"}},
		{name: "external ID without role", options: Options{ExternalID: "tenant/acme"}, wantErr: true},
		{name: "user ARN", options: Options{RoleARN: "arn:aws:iam::123456789012:user/primary", RoleSessionName: "primary-local"}, wantErr: true},
		{name: "empty role", options: Options{RoleARN: "arn:aws:iam::123456789012:role/", RoleSessionName: "primary-local"}, wantErr: true},
		{name: "bad session", options: Options{RoleARN: "arn:aws:iam::123456789012:role/primary", RoleSessionName: "spaces are rejected"}, wantErr: true},
		{name: "bad external ID", options: Options{RoleARN: "arn:aws:iam::123456789012:role/primary", RoleSessionName: "primary-local", ExternalID: "x"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validate(normalized(test.options))
			if (err != nil) != test.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestNormalizedDefaultsRoleSessionName(t *testing.T) {
	options := normalized(Options{RoleARN: " arn:aws:iam::123456789012:role/primary "})
	if options.RoleARN != "arn:aws:iam::123456789012:role/primary" || options.RoleSessionName != defaultRoleSessionName {
		t.Fatalf("options = %+v", options)
	}
}
