package util

import (
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

func DeleteMessageAfterTime(session *discordgo.Session, channelID string, messageID string, timeDelay time.Duration) error {
	message, err := session.ChannelMessage(channelID, messageID)
	if err != nil {
		return err
	}

	time.AfterFunc(timeDelay, func() {
		_ = session.ChannelMessageDelete(channelID, message.ID)
	})

	return nil
}

func GetVoiceChannelMemberCount(s *discordgo.Session, guildID, channelID string) (int, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		return 0, fmt.Errorf("Getting guild: %w", err)
	}

	memberCount := 0

	// Loop through VoiceStates to find all members in the specific voice channel
	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == channelID {
			memberCount++
		}
	}

	return memberCount, nil
}
