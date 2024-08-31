package embeds

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

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

func SuccessfulAudioFileUploadEmbed(memberCreatedFor *discordgo.Member, memberCreatedBy *discordgo.Member, audioType string, url string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: fmt.Sprintf("🎤 Voiceline %s successfully created 🎤", audioType),
		Color: 0x67e9ff,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:  "",
				Value: fmt.Sprintf("%s new [Voiceline](%s) 🎤!", memberCreatedFor.User.Username, url),
			},
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: memberCreatedFor.AvatarURL(""),
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text:    fmt.Sprintf("Created by: %s", memberCreatedBy.DisplayName()),
			IconURL: memberCreatedBy.AvatarURL(""),
		},
	}
}
