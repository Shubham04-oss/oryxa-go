package provision

import "slices"

func cliAppConfig(config Config) Config {
	cli := config
	cli.AppClientName = config.PoolName + "-cli"
	cli.CallbackURLs = []string{config.CLICallbackURL}
	for _, callback := range config.CLICallbackURLs {
		if !slices.Contains(cli.CallbackURLs, callback) {
			cli.CallbackURLs = append(cli.CallbackURLs, callback)
		}
	}
	cli.LogoutURLs = nil
	return cli
}
