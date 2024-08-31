package greeter

import (
	"context"
	"fmt"
	"io"
	"salutations/internal/embeds"
	firebaseAdapter "salutations/internal/firebase"
	util "salutations/pkg/util"
	"sync"

	"github.com/bwmarrin/discordgo"
	"golang.org/x/exp/rand"
	"golang.org/x/sync/errgroup"

	"github.com/google/uuid"
	"github.com/jonas747/dca"
	"github.com/kkdai/youtube/v2"
	"go.uber.org/zap"
)

const (
	WELCOME_COLLECTION string = "welcomeIntros"
	BUCKET_NAME               = "twitterbot-e7ab0.appspot.com"
)

type FileType string

const (
	mp3 FileType = "audio/mpeg"
	mp4 FileType = "audio/mp4"
	zip FileType = "application/zip"
)

type voiceState string

const (
	PLAYING     voiceState = "PLAYING"
	NOT_PLAYING voiceState = "NOT_PLAYING"
)

type guildPlayer struct {
	guildID     string
	voiceClient *discordgo.VoiceConnection
	queue       []string
	voiceState  voiceState
	stream      *dca.StreamingSession
}

type greeterRunner struct {
	firebaseAdapter     firebaseAdapter.Firebase
	audioQueue          map[string]string
	logger              *zap.Logger
	ytdlClient          *youtube.Client
	songSignal          chan *guildPlayer
	guildPlayerMappings map[string]*guildPlayer
	guildsMutex         sync.RWMutex
}

var commands []*discordgo.ApplicationCommand = []*discordgo.ApplicationCommand{{
	Name:        "upload",
	Description: "Upload a voiceline for a user from your server",
	Options: []*discordgo.ApplicationCommandOption{
		{
			Name:        "member",
			Description: "The member you wish to create a voiceline for",
			Type:        discordgo.ApplicationCommandOptionUser,
			Required:    true,
		},
		{
			Name:        "type",
			Type:        discordgo.ApplicationCommandOptionString,
			Description: "The type of voiceline you are creating",
			Required:    true,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{
					Name:  "Intro",
					Value: "intro",
				},
				{
					Name:  "Outro",
					Value: "outro",
				},
			},
		},
		{
			Name:        "file",
			Type:        discordgo.ApplicationCommandOptionAttachment,
			Required:    true,
			Description: "The audio files/zips you wish to upload",
		},
	},
}}

func NewGreeterRunner(logger *zap.Logger, ytdlClient *youtube.Client, firebaseAdapter firebaseAdapter.Firebase) (*greeterRunner, error) {
	songSignals := make(chan *guildPlayer)
	greeter := &greeterRunner{
		firebaseAdapter:     firebaseAdapter,
		audioQueue:          make(map[string]string),
		logger:              logger,
		ytdlClient:          ytdlClient,
		songSignal:          songSignals,
		guildPlayerMappings: make(map[string]*guildPlayer),
		guildsMutex:         sync.RWMutex{},
	}
	go greeter.globalPlay()
	return greeter, nil
}

func (g *greeterRunner) RegisterCommands(s *discordgo.Session) error {
	for _, command := range commands {
		_, err := s.ApplicationCommandCreate(s.State.Application.ID, "", command)
		if err != nil {
			return err
		}
	}
	s.AddHandler(g.greeterHandler)
	s.AddHandler(g.voiceStateUpdate)
	return nil

}

func (g *greeterRunner) globalPlay() {
	for gp := range g.songSignal {
		go g.playAudio(gp)
	}
}

func (g *greeterRunner) getAudioFileURL(audioFileName string) (string, error) {
	return g.firebaseAdapter.GenerateSignedUrl(BUCKET_NAME, fmt.Sprintf("voicelines/%s", audioFileName))
}

func (g *greeterRunner) voiceStateUpdate(s *discordgo.Session, vc *discordgo.VoiceStateUpdate) {
	hasJoined := vc.BeforeUpdate == nil && !vc.VoiceState.Member.User.Bot && vc.VoiceState != nil
	if hasJoined {
		ctx := context.Background()
		if _, ok := g.guildPlayerMappings[vc.GuildID]; !ok {
			channelVoiceConnection, err := s.ChannelVoiceJoin(vc.GuildID, vc.ChannelID, false, true)
			if err != nil {
				g.logger.Error("error unable to join voice channel", zap.String("channel_id", vc.ChannelID), zap.String("guild_id", vc.GuildID), zap.Error(err))
				return
			}
			g.guildPlayerMappings[vc.GuildID] = &guildPlayer{
				guildID:     vc.GuildID,
				voiceClient: channelVoiceConnection,
				queue:       []string{},
				voiceState:  NOT_PLAYING,
			}
		}

		randomAudioTrack, err := g.retrieveRandomAudioName(ctx, WELCOME_COLLECTION, vc.UserID)
		if err != nil {
			g.logger.Error("failed to get random audio track from firestore", zap.Error(err))
			return
		}
		audioBytes, err := g.firebaseAdapter.DownloadFileBytes(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%v", randomAudioTrack))

		if err != nil {
			g.logger.Error("failed to get audio bytes from storage", zap.Error(err))
			return
		}
		filePath, err := util.MakeDirectoryAndFile(randomAudioTrack)
		if err != nil {
			g.logger.Error("failed to get make temp directory and file", zap.Error(err))
			return

		}
		if err := util.WriteFile(audioBytes, filePath); err != nil {
			g.logger.Error("failed to write contents to file", zap.Error(err))
			return
		}

		g.guildsMutex.RLock()
		g.guildPlayerMappings[vc.GuildID].queue = append(g.guildPlayerMappings[vc.GuildID].queue, filePath)
		g.guildsMutex.RUnlock()
		if g.guildPlayerMappings[vc.GuildID].voiceState == NOT_PLAYING {
			g.songSignal <- g.guildPlayerMappings[vc.GuildID]
		}
	}
}

func (g *greeterRunner) retrieveRandomAudioName(ctx context.Context, collection string, userId string) (string, error) {
	data, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, userId)
	if err != nil {
		return "", err
	}
	var audioListKey string
	if collection == WELCOME_COLLECTION {
		audioListKey = "intro_array"
	} else {
		audioListKey = "outro_array"
	}

	if audio_array, ok := data[audioListKey]; ok {
		if audio_array_slice, ok := audio_array.([]interface{}); ok {
			randomIndex := rand.Intn(len(audio_array_slice))
			if recordMap, ok := audio_array_slice[randomIndex].(map[string]interface{}); ok {
				return recordMap["track_name"].(string), nil
			}
		}
	}

	return "", nil
}

func (g *greeterRunner) playAudio(guildPlayer *guildPlayer) {
	if guildPlayer.voiceClient == nil || len(guildPlayer.queue) == 0 {
		return
	}
	g.guildsMutex.Lock()
	guildPlayer.voiceState = PLAYING
	audioPath := guildPlayer.queue[0]
	guildPlayer.queue = guildPlayer.queue[1:]
	g.guildsMutex.Unlock()

	defer util.DeleteFile(audioPath)

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 128

	es, err := dca.EncodeFile(audioPath, opts)
	if err != nil {
		g.logger.Error(err.Error())
		return
	}

	defer es.Cleanup()
	doneChan := make(chan error)
	guildPlayer.stream = dca.NewStream(es, guildPlayer.voiceClient, doneChan)
	guildPlayer.voiceState = PLAYING
	for err := range doneChan {
		if err != nil && err != io.EOF {
			g.logger.Error(err.Error())
			return
		}
		g.guildsMutex.Lock()
		if len(guildPlayer.queue) > 0 {
			g.songSignal <- guildPlayer
		} else {
			guildPlayer.voiceState = NOT_PLAYING
		}
		g.guildsMutex.Unlock()
	}

}

func (g *greeterRunner) upload(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	options := i.ApplicationCommandData().Options
	_, _ = options[0].UserValue(s), options[1].StringValue()
	fileAttachment := i.ApplicationCommandData().Resolved.Attachments
	ctx := context.Background()
	for _, file := range fileAttachment {
		switch FileType(file.ContentType) {
		case mp3, mp4:
			file, err := util.DownloadFileToTempDirectory(file.URL)
			if err != nil {
				g.logger.Error("error attempting to download temporary file", zap.Error(err))
				return err
			}
			defer file.Close()
			uuid, _ := uuid.NewV7()

			err = g.firebaseAdapter.UploadFileToStorage(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%s", uuid), file, uuid.String())
			if err != nil {
				g.logger.Error("error attempting to upload to firebase", zap.Error(err))
				return err
			}
		case zip:
			file, err := util.DownloadFileToTempDirectory(file.URL)
			if err != nil {
				g.logger.Error("error attempting to download temporary file", zap.Error(err))
				return err
			}
			defer file.Close()
			fileList, err := util.Unzip(file.Name(), "temp")
			if err != nil {
				g.logger.Error("error unzipping inputted zip", zap.Error(err))
				return err
			}
			eg, ctx := errgroup.WithContext(ctx)
			for _, file := range fileList {
				eg.Go(func() error {
					uuid, _ := uuid.NewV7()
					return g.firebaseAdapter.UploadFileToStorage(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%s", uuid), file, uuid.String())
				})
			}
			if err = eg.Wait(); err != nil {
				g.logger.Error("error uploading to firebase storage", zap.Error(err))
				return err
			}

		default:
			_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{
					embeds.ErrorMessageEmbed("File must be an mp3 or m4a file!"),
				},
			})
			if err != nil {
				g.logger.Error("unable to send follow up embed: %v", zap.Error(err))
				return err
			}

		}

	}
	return nil
}

func (g *greeterRunner) greeterHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Name {
	case "upload":
		g.upload(s, i)
	}
}
