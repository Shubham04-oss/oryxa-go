package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/Shubham04-oss/oryxa-go/auth/infra/cognito/internal/provision"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := provision.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}

	output, err := provision.Run(ctx, cognitoidentityprovider.NewFromConfig(awsConfig), config)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		log.Fatalf("write provision output: %v", err)
	}
}
