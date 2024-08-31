package embeds

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

func ErrorMessageEmbed(msg string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "❌ **Invalid usage**",
		Description: msg,
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
			Text:    "Created by: " + memberCreatedBy.DisplayName(),
			IconURL: memberCreatedBy.AvatarURL(""),
		},
	}
}

func SuccessfulAudioZipUploadEmbed(memberCreatedFor *discordgo.Member, memberCreatedBy *discordgo.Member, audioType string, urls []string) *discordgo.MessageEmbed {
	embedFields := []*discordgo.MessageEmbedField{}

	for i, url := range urls {
		embedFields = append(embedFields, &discordgo.MessageEmbedField{
			Name:   "",
			Value:  fmt.Sprintf("%s's new [Voiceline %d](%s) 🎤!", memberCreatedFor.User.Username, i+1, url),
			Inline: false,
		})
	}

	return &discordgo.MessageEmbed{
		Title: fmt.Sprintf("🎤 %d Voiceline %ss Successfully Created 🎤", len(urls), audioType),
		Color: 0x67e9ff,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: memberCreatedFor.AvatarURL(""),
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text:    "Created by: " + memberCreatedBy.DisplayName(),
			IconURL: memberCreatedBy.AvatarURL(""),
		},
		Fields: embedFields,
	}
}

func UnexpectedErrorEmbed() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: "Oops something went wrong, please try again later!",
		Color: 0x992D22,
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://media.giphy.com/media/l3vR7SWnEv6mmhS0g/giphy.gif",
		},
	}
}
