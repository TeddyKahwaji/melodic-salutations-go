package greeter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	cogs "salutations/internal/cogs"
	"salutations/internal/embeds"
	firebaseAdapter "salutations/internal/firebase"
	util "salutations/pkg/util"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/exp/rand"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"
	"github.com/jonas747/dca"
	"github.com/kkdai/youtube/v2"
	"go.uber.org/zap"
)

const (
	WELCOME_COLLECTION string = "welcomeIntros"
	OUTRO_COLLECTION   string = "byeOutros"
	BUCKET_NAME        string = "twitterbot-e7ab0.appspot.com"
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

type trackRecord struct {
	AddedBy   string    `firestore:"added_by"`
	CreatedAt time.Time `firestore:"created_at"`
	TrackName string    `firestore:"track_name"`
}
type firebaseIntroRecord struct {
	Name       string        `firestore:"name"`
	IntroArray []trackRecord `firestore:"intro_array"`
}

type firebaseOutroRecord struct {
	Name       string        `firestore:"name"`
	OutroArray []trackRecord `firestore:"outro_array"`
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

func NewGreeterRunner(logger *zap.Logger, ytdlClient *youtube.Client, firebaseAdapter firebaseAdapter.Firebase) (cogs.Cogs, error) {
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

func (g *greeterRunner) GetCommands() []*discordgo.ApplicationCommand {
	return commands
}

func (g *greeterRunner) RegisterCommands(s *discordgo.Session) error {

	if _, err := s.ApplicationCommandBulkOverwrite(s.State.Application.ID, "", commands); err != nil {
		return err
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

func (g *greeterRunner) voiceStateUpdate(s *discordgo.Session, vc *discordgo.VoiceStateUpdate) {
	hasJoined := vc.BeforeUpdate == nil && !vc.VoiceState.Member.User.Bot && vc.ChannelID != ""
	hasLeft := vc.BeforeUpdate != nil && !vc.Member.User.Bot && vc.ChannelID == ""

	if hasLeft {
		channel, err := s.Channel(vc.BeforeUpdate.ChannelID)
		if err != nil {
			g.logger.Error("error getting channel", zap.Error(err), zap.String("channel_id", vc.BeforeUpdate.ChannelID))
			return
		}
		if channel.MemberCount == 0 && vc.VoiceState != nil {
			if vc, ok := s.VoiceConnections[vc.GuildID]; ok {
				g.guildsMutex.Lock()
				vc.Disconnect()
				delete(g.guildPlayerMappings, vc.GuildID)
				g.guildsMutex.Unlock()
			}
		}
		return
	}

	var COLLECTION string
	if hasJoined {
		COLLECTION = WELCOME_COLLECTION
	} else {
		COLLECTION = OUTRO_COLLECTION
	}

	ctx := context.Background()
	if hasJoined || hasLeft {
		g.guildsMutex.Lock()
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

		randomAudioTrack, err := g.retrieveRandomAudioName(ctx, COLLECTION, vc.UserID)
		if err != nil {
			g.logger.Error("failed to get random audio track from firestore", zap.Error(err))
			return
		}
		audioBytes, err := g.firebaseAdapter.DownloadFileBytes(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%s", randomAudioTrack))
		if err != nil {
			g.logger.Error("failed to get audio bytes from storage", zap.Error(err))
			return
		}

		file, err := util.DownloadFileToTempDirectory(audioBytes)
		if err != nil {
			g.logger.Error("failed to download audio bytes to temporary directory", zap.Error(err))
			return
		}

		g.guildPlayerMappings[vc.GuildID].queue = append(g.guildPlayerMappings[vc.GuildID].queue, file.Name())
		g.guildsMutex.Unlock()
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

	defer func() {
		if err := util.DeleteFile(audioPath); err != nil {
			g.logger.Error("error trying to delete file", zap.Error(err), zap.String("file_name", audioPath))
		}
	}()

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 128

	es, err := dca.EncodeFile(audioPath, opts)
	if err != nil {
		g.logger.Error("error encoding file", zap.Error(err))
		return
	}

	defer es.Cleanup()
	doneChan := make(chan error)
	guildPlayer.stream = dca.NewStream(es, guildPlayer.voiceClient, doneChan)
	guildPlayer.voiceState = PLAYING
	for err := range doneChan {
		if err != nil && err != io.EOF {
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
	memberID, audioType := options[0].Value.(string), options[1].Value.(string)
	member, err := s.State.Member(i.GuildID, memberID)
	if err != nil {
		g.logger.Error("error getting member to create audio track for", zap.Error(err), zap.String("user_id", memberID))
		return err
	}
	audioListKey := "outro_array"
	collection := OUTRO_COLLECTION
	if audioType == "intro" {
		collection = WELCOME_COLLECTION
		audioListKey = "intro_array"
	}

	fileAttachment := i.ApplicationCommandData().Resolved.Attachments
	ctx := context.Background()
	for _, file := range fileAttachment {
		switch FileType(file.ContentType) {
		case mp3, mp4:
			resp, err := http.Get(file.URL)
			if err != nil {
				g.logger.Error("error attempting to download discord file", zap.Error(err))
				return err
			}

			file, err := util.DownloadFileToTempDirectory(resp.Body)
			defer func() {
				if err := resp.Body.Close(); err != nil {
					g.logger.Error("error closing body", zap.Error(err))
				}
				if err := file.Close(); err != nil {
					g.logger.Error("error closing file", zap.Error(err))
				}
				if err := util.DeleteFile(file.Name()); err != nil {
					g.logger.Error("error trying to delete file", zap.Error(err), zap.String("file_name", file.Name()))
				}

			}()

			if err != nil {
				g.logger.Error("error attempting to download temporary file", zap.Error(err))
				return err
			}

			uuid, _ := uuid.NewV7()
			if err := g.firebaseAdapter.UploadFileToStorage(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%s", uuid.String()), file, uuid.String()); err != nil {
				g.logger.Error("error attempting to upload to firebase", zap.Error(err))
				return err
			}

			data := map[string]interface{}{
				audioListKey: firestore.ArrayUnion(map[string]string{"track_name": uuid.String(),
					"created_at": time.Now().String(),
					"added_by":   i.Member.User.ID}),
				"name": memberID,
			}

			if err := g.firebaseAdapter.UpdateDocument(ctx, collection, memberID, data); err != nil {
				g.logger.Error("error updating document", zap.Error(err), zap.String("collection", collection), zap.String("user_id", memberID), zap.Any("data", data))
				return err
			}

			signedUrl, err := g.firebaseAdapter.GenerateSignedUrl(BUCKET_NAME, fmt.Sprintf("voicelines/%s", uuid.String()))
			if err != nil {
				g.logger.Error("error generating signed url", zap.Error(err), zap.String("member_created_for", member.User.ID), zap.String("member_created_by", i.Member.User.ID))
				return err
			}
			_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{
					embeds.SuccessfulAudioFileUploadEmbed(member, i.Member, audioType, signedUrl),
				},
			})
			if err != nil {
				g.logger.Error("error unable to send follow up embed: %v", zap.Error(err))
				return err
			}
		case zip:
			resp, err := http.Get(file.URL)
			if err != nil {
				g.logger.Error("error attempting to download discord file", zap.Error(err))
				return err
			}
			defer resp.Body.Close()
			file, err := util.DownloadFileToTempDirectory(resp.Body)
			if err != nil {
				g.logger.Error("error attempting to download temporary file", zap.Error(err))
				return err
			}

			fileList, err := util.Unzip(file.Name(), util.GetDirectoryFromFileName(file.Name()))
			if err != nil {
				g.logger.Error("error unzipping inputted zip", zap.Error(err))
				return err
			}

			if _, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, memberID); err != nil {
				if status.Code(err) == codes.NotFound {
					var err error
					if collection == WELCOME_COLLECTION {
						err = g.firebaseAdapter.CreateDocument(ctx, collection, memberID, firebaseIntroRecord{Name: memberID, IntroArray: []trackRecord{}})
					} else {
						err = g.firebaseAdapter.CreateDocument(ctx, collection, memberID, firebaseOutroRecord{Name: memberID, OutroArray: []trackRecord{}})
					}

					if err != nil {
						g.logger.Error("error creating firestore document", zap.Error(err), zap.String("user_id", memberID), zap.String("collection", collection))
						return err
					}
				} else {
					return err
				}
			}

			eg, ctx := errgroup.WithContext(ctx)
			for _, file := range fileList {
				eg.Go(func() error {
					f, err := os.Open(file.Name())
					if err != nil {
						return err
					}
					defer func() {
						if err := f.Close(); err != nil {
							g.logger.Error("error closing file", zap.Error(err))
						}
						if err := util.DeleteFile(f.Name()); err != nil {
							g.logger.Error("error trying to delete file", zap.Error(err), zap.String("file_name", f.Name()))
						}
					}()

					uuid, _ := uuid.NewV7()
					if err := g.firebaseAdapter.UploadFileToStorage(ctx, BUCKET_NAME, fmt.Sprintf("voicelines/%s", uuid), file, uuid.String()); err != nil {
						return err
					}

					data := map[string]interface{}{
						audioListKey: firestore.ArrayUnion(trackRecord{
							TrackName: uuid.String(),
							CreatedAt: time.Now(),
							AddedBy:   i.Member.User.ID}),
					}
					return g.firebaseAdapter.UpdateDocument(ctx, collection, memberID, data)
				})
			}
			if err = eg.Wait(); err != nil {
				g.logger.Error("error creating or uploading files", zap.Error(err))
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
