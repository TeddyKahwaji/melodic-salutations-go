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

func GetPaginationComponent(disableStart bool, disablePrevious bool, disableNext bool, disableFinish bool) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "|<",
					Style:    discordgo.SuccessButton,
					CustomID: "first",
					Disabled: disableStart,
				},
				discordgo.Button{
					Label:    "<",
					Style:    discordgo.PrimaryButton,
					CustomID: "prev",
					Disabled: disablePrevious,
				},
				discordgo.Button{
					Label:    ">",
					Style:    discordgo.PrimaryButton,
					CustomID: "next",
					Disabled: disableNext,
				},
				discordgo.Button{
					Label:    ">|",
					Style:    discordgo.SuccessButton,
					CustomID: "last",
					Disabled: disableFinish,
				},
			},
		},
	}
}

func NoDataForMemberEmbed(audioType string, memberName string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: fmt.Sprintf("There are no %s voicelines for %s", audioType, memberName),
		Color: 0x206694,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:  "",
				Value: fmt.Sprintf("Upload a voiceline for **%s** with </upload:1098009652706426911>", memberName),
			},
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: "https://media.giphy.com/media/S5tkhUBHTTWh865paS/giphy.gif",
		},
	}
}

func GetSuccessfulAudioRetrievalEmbeds(member *discordgo.Member, audioType string, urls []string) []*discordgo.MessageEmbed {
	embedFields := []*discordgo.MessageEmbedField{}

	for i, url := range urls {
		embedFields = append(embedFields, &discordgo.MessageEmbedField{
			Name:  "",
			Value: fmt.Sprintf("`%d:` [%s #%d](%s)", i+1, member.User.Username, i+1, url),
		})
	}

	embedList := []*discordgo.MessageEmbed{}

	for i := 0; i < len(urls); i += 4 {
		endBound := min(len(embedFields), i+4)
		embedList = append(embedList, &discordgo.MessageEmbed{
			Title: fmt.Sprintf("%s's Voiceline %ss", member.User.Username, audioType),
			Color: 0x67e9ff,
			Thumbnail: &discordgo.MessageEmbedThumbnail{
				URL: member.AvatarURL(""),
			},
			Fields: embedFields[i:endBound],
		})
	}

	return embedList
}

func HelpMenuEmbed() *discordgo.MessageEmbed {
	commandsToDescription := map[string]string{
		"📽️ Upload":    "Upload an outro/intro voiceline (.zip, .mp3, .m4a) for a given user",
		"🎤 Voicelines": "View the intro/outro voicelines for a given user",
	}

	embed := &discordgo.MessageEmbed{
		Title:       "**🤖 Melodic Salutation's Help Page 👋**",
		Description: "I only supports `/` commands, to view available commands use `/` followed by the desired command",
		Color:       0x67e9ff,
	}

	for commandName, commandDescription := range commandsToDescription {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("**%s**", commandName),
			Value: fmt.Sprintf("``%s``", commandDescription),
		})
	}
	return embed
}
