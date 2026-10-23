package cli

// rootCommand is the whole command tree, in the groups `stackstead help`
// shows: the server itself, the remote client, and host-side administration.
func rootCommand() *Command {
	var all []*Command
	add := func(group string, cmds ...*Command) {
		for _, c := range cmds {
			c.Group = group
			all = append(all, c)
		}
	}
	add("Server", serveCommand(), versionCommand())
	add("Remote",
		loginCommand(), logoutCommand(), contextCommand(), whoamiCommand(), statusCommand(),
		stacksCommand(), opsCommand(), logsCommand(), execCommand(), metricsCommand(), eventsCommand(),
		networksCommand(), storageCommand(), imagesCommand(), settingsCommand())
	add("Host admin", configCommand(), dbCommand(), adminCommand(), healthCommand(), upgradeCommand())
	return &Command{
		Name:  "stackstead",
		Short: "Stackstead: manage Docker Compose stacks",
		Sub:   all,
	}
}
