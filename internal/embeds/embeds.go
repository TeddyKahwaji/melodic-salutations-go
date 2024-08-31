package embeds

import "github.com/bwmarrin/discordgo"

func ErrorMessageEmbed(msg string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "❌ **Invalid usage**",
		Description: "File must be an mp3 or m4a file!",
		Color:       0x992D22,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://media.giphy.com/media/11e5gZ6NJ8AB1K/giphy.gif",
		},
	}
}
