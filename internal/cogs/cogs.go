package cogs

import "github.com/bwmarrin/discordgo"

type Cogs interface {
	RegisterCommands(s *discordgo.Session) error
	GetCommands() []*discordgo.ApplicationCommand
}
