package greeter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	cogs "salutations/internal/cogs"
	"salutations/internal/embeds"
	firebaseAdapter "salutations/internal/firebase"
	util "salutations/pkg/util"

	"cloud.google.com/go/firestore"
	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"github.com/jonas747/dca"
	"github.com/kkdai/youtube/v2"
	"go.uber.org/zap"
	"golang.org/x/exp/rand"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	WelcomeCollection string = "welcomeIntros"
	OutroCollection   string = "byeOutros"
	IntroArrayKey     string = "intro_array"
	OutroArrayKey     string = "outro_array"
	BucketName        string = "twitterbot-e7ab0.appspot.com"
)

type FileType string

const (
	mp3 FileType = "audio/mpeg"
	mp4 FileType = "audio/mp4"
	zip FileType = "application/zip"
)

type voiceState string

const (
	Playing    voiceState = "PLAYING"
	NotPlaying voiceState = "NOT_PLAYING"
)

type paginationState struct {
	CurrentPage int
	Pages       []*discordgo.MessageEmbed
}

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
	sync.RWMutex
	messageStore map[string]*paginationState
}

type trackRecord struct {
	AddedBy   string    `firestore:"added_by"    mapstructure:"added_by"`
	CreatedAt time.Time `firestore:"created_at"  mapstructure:"created_at"`
	TrackName string    `firestore:"track_name"  mapstructure:"track_name"`
}

type firebaseIntroRecord struct {
	Name       string        `firestore:"name"`
	IntroArray []trackRecord `firestore:"intro_array"`
}

type firebaseOutroRecord struct {
	Name       string        `firestore:"name"`
	OutroArray []trackRecord `firestore:"outro_array"`
}

func NewGreeterRunner(logger *zap.Logger, ytdlClient *youtube.Client, firebaseAdapter firebaseAdapter.Firebase) (cogs.Cogs, error) {
	songSignals := make(chan *guildPlayer)
	greeter := &greeterRunner{
		firebaseAdapter:     firebaseAdapter,
		audioQueue:          make(map[string]string),
		logger:              logger,
		ytdlClient:          ytdlClient,
		songSignal:          songSignals,
		guildPlayerMappings: make(map[string]*guildPlayer),
		messageStore:        make(map[string]*paginationState),
	}

	go greeter.globalPlay()

	return greeter, nil
}

func (g *greeterRunner) GetCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
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
		},
		{
			Name:        "voicelines",
			Description: "View the voicelines of a user from your server",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "member",
					Description: "A member from your server",
					Type:        discordgo.ApplicationCommandOptionUser,
					Required:    true,
				},
				{
					Name:        "type",
					Type:        discordgo.ApplicationCommandOptionString,
					Description: "The voiceline type you would like view",
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
			},
		},
		{
			Name:        "help",
			Description: "Displays the command menu",
		},
	}
}

func (g *greeterRunner) RegisterCommands(session *discordgo.Session) error {
	if _, err := session.ApplicationCommandBulkOverwrite(session.State.Application.ID, "", g.GetCommands()); err != nil {
		return err
	}

	session.AddHandler(g.greeterHandler)
	session.AddHandler(g.voiceUpdate)
	session.AddHandler(g.messageComponentHandler)
	return nil
}

func (g *greeterRunner) globalPlay() {
	for gp := range g.songSignal {
		go g.playAudio(gp)
	}
}

func (g *greeterRunner) voiceUpdate(session *discordgo.Session, vc *discordgo.VoiceStateUpdate) {
	hasJoined := vc.BeforeUpdate == nil && !vc.VoiceState.Member.User.Bot && vc.ChannelID != ""
	hasLeft := vc.BeforeUpdate != nil && !vc.Member.User.Bot

	if hasLeft {
		g.Lock() // Lock before accessing shared data

		perms, err := session.UserChannelPermissions(session.State.Ready.User.ID, vc.BeforeUpdate.ChannelID)
		if err != nil {
			g.logger.Error("unable to get permissions for channel", zap.Error(err), zap.String("channel_id", vc.BeforeUpdate.ChannelID))
			g.Unlock() // Unlock before returning
			return
		}

		if perms&discordgo.PermissionVoiceConnect == 0 || perms&discordgo.PermissionVoiceSpeak == 0 {
			g.logger.Info("Bot will not be joining voice channel because they do not have sufficient privileges", zap.String("channel_id", vc.BeforeUpdate.ChannelID))
			g.Unlock() // Unlock before returning
			return
		}

		channel, err := session.Channel(vc.BeforeUpdate.ChannelID)
		if err != nil {
			g.logger.Error("error getting channel", zap.Error(err), zap.String("channel_id", vc.BeforeUpdate.ChannelID))
			g.Unlock() // Unlock before returning
			return
		}

		if channel.MemberCount == 0 && vc.VoiceState != nil {
			if vc, ok := session.VoiceConnections[vc.GuildID]; ok {
				if err := vc.Disconnect(); err != nil {
					g.logger.Error("error disconnecting from channel", zap.Error(err), zap.String("channel_id", vc.ChannelID))
					g.Unlock() // Unlock before returning
					return
				}

				delete(g.guildPlayerMappings, vc.GuildID)
			}
		}

		g.Unlock() // Unlock after modifications are done
		return
	}

	var COLLECTION string
	if hasJoined {
		COLLECTION = WelcomeCollection
	} else {
		COLLECTION = OutroCollection
	}

	ctx := context.Background()
	if hasJoined || hasLeft {
		g.Lock() // Lock before accessing shared data

		if _, ok := g.guildPlayerMappings[vc.GuildID]; !ok {
			perms, err := session.UserChannelPermissions(session.State.Ready.User.ID, vc.ChannelID)
			if err != nil {
				g.logger.Error("unable to get permissions for channel", zap.Error(err), zap.String("channel_id", vc.ChannelID))
				g.Unlock() // Unlock before returning
				return
			}

			if perms&int64(discordgo.PermissionVoiceConnect) == 0 || perms&int64(discordgo.PermissionVoiceSpeak) == 0 {
				g.logger.Info("Bot will not be joining voice channel because they do not have sufficient privileges", zap.String("channel_id", vc.ChannelID))
				g.Unlock() // Unlock before returning
				return
			}

			channelVoiceConnection, err := session.ChannelVoiceJoin(vc.GuildID, vc.ChannelID, false, true)
			if err != nil {
				g.logger.Error("error unable to join voice channel", zap.String("channel_id", vc.ChannelID), zap.String("guild_id", vc.GuildID), zap.Error(err))
				g.Unlock() // Unlock before returning
				return
			}

			// Modify the shared data (g.guildPlayerMappings)
			g.guildPlayerMappings[vc.GuildID] = &guildPlayer{
				guildID:     vc.GuildID,
				voiceClient: channelVoiceConnection,
				queue:       []string{},
				voiceState:  NotPlaying,
			}
		}

		randomAudioTrack, err := g.retrieveRandomAudioName(ctx, COLLECTION, vc.UserID)
		if err != nil {
			g.logger.Error("failed to get random audio track from firestore", zap.Error(err))
			g.Unlock() // Unlock before returning
			return
		}

		audioBytes, err := g.firebaseAdapter.DownloadFileBytes(ctx, BucketName, fmt.Sprintf("voicelines/%s", randomAudioTrack))
		if err != nil {
			g.logger.Error("failed to get audio bytes from storage", zap.Error(err))
			g.Unlock() // Unlock before returning
			return
		}

		file, err := util.DownloadFileToTempDirectory(audioBytes)
		if err != nil {
			g.logger.Error("failed to download audio bytes to temporary directory", zap.Error(err))
			g.Unlock() // Unlock before returning
			return
		}

		// Update the queue for the guild
		g.guildPlayerMappings[vc.GuildID].queue = append(g.guildPlayerMappings[vc.GuildID].queue, file.Name())
		g.Unlock() // Unlock after modifications are done

		if g.guildPlayerMappings[vc.GuildID].voiceState == NotPlaying {
			g.songSignal <- g.guildPlayerMappings[vc.GuildID]
		}
	}
}

// TODO: Reduce complexity here.
func (g *greeterRunner) retrieveRandomAudioName(ctx context.Context, collection string, userId string) (string, error) {
	data, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, userId)
	if err != nil {
		return "", err
	}

	audioListKey := OutroArrayKey
	if collection == WelcomeCollection {
		audioListKey = IntroArrayKey
	}

	if audioArray, ok := data[audioListKey]; ok {
		if audioSlice, ok := audioArray.([]interface{}); ok {
			randomIndex := rand.Intn(len(audioSlice))
			if recordMap, ok := audioSlice[randomIndex].(map[string]interface{}); ok {
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

	g.Lock()
	guildPlayer.voiceState = Playing
	audioPath := guildPlayer.queue[0]
	guildPlayer.queue = guildPlayer.queue[1:]
	g.Unlock()

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
	guildPlayer.voiceState = Playing

	for err := range doneChan {
		if err != nil && errors.Is(err, io.EOF) {
			return
		}

		g.Lock()
		if len(guildPlayer.queue) > 0 {
			g.songSignal <- guildPlayer
		} else {
			guildPlayer.voiceState = NotPlaying
		}
		g.Unlock()
	}
}

func (g *greeterRunner) upload(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		g.logger.Error("error attempting to defer message in upload command", zap.Error(err))
		return err
	}

	options := interaction.ApplicationCommandData().Options
	memberID, audioType := options[0].Value.(string), options[1].Value.(string)

	member, err := session.State.Member(interaction.GuildID, memberID)
	if err != nil {
		g.logger.Error("error getting member to create audio track for", zap.Error(err), zap.String("user_id", memberID))
		return err
	}

	audioListKey := "outro_array"
	collection := OutroCollection

	if audioType == "intro" {
		collection = WelcomeCollection
		audioListKey = "intro_array"
	}

	fileAttachment := interaction.ApplicationCommandData().Resolved.Attachments
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
			if err := g.firebaseAdapter.UploadFileToStorage(ctx, BucketName, fmt.Sprintf("voicelines/%s", uuid.String()), file, uuid.String()); err != nil {
				g.logger.Error("error attempting to upload to firebase", zap.Error(err))
				return err
			}

			data := map[string]interface{}{
				audioListKey: firestore.ArrayUnion(map[string]string{
					"track_name": uuid.String(),
					"created_at": time.Now().String(),
					"added_by":   interaction.Member.User.ID,
				}),
				"name": memberID,
			}

			if err := g.firebaseAdapter.UpdateDocument(ctx, collection, memberID, data); err != nil {
				g.logger.Error("error updating document", zap.Error(err), zap.String("collection", collection), zap.String("user_id", memberID), zap.Any("data", data))
				return err
			}

			signedURL, err := g.firebaseAdapter.GenerateSignedURL(BucketName, fmt.Sprintf("voicelines/%s", uuid.String()))
			if err != nil {
				g.logger.Error("error generating signed url", zap.Error(err), zap.String("member_created_for", member.User.ID), zap.String("member_created_by", interaction.Member.User.ID))
				return err
			}
			_, err = session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{
					embeds.SuccessfulAudioFileUploadEmbed(member, interaction.Member, audioType, signedURL),
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
					if collection == WelcomeCollection {
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

			urlsCreated := []string{}

			eg, ctx := errgroup.WithContext(ctx)
			for _, file := range fileList {
				eg.Go(func() error {
					f, err := os.Open(file.Name())
					if err != nil {
						return fmt.Errorf("error opening file: %w", err)
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
					if err := g.firebaseAdapter.UploadFileToStorage(ctx, BucketName, fmt.Sprintf("voicelines/%s", uuid), file, uuid.String()); err != nil {
						return fmt.Errorf("error uploading to file storage %w", err)
					}

					data := map[string]interface{}{
						audioListKey: firestore.ArrayUnion(trackRecord{
							TrackName: uuid.String(),
							CreatedAt: time.Now(),
							AddedBy:   interaction.Member.User.ID,
						}),
					}

					if err := g.firebaseAdapter.UpdateDocument(ctx, collection, memberID, data); err != nil {
						return fmt.Errorf("error updating document %w", err)
					}

					// TODO: Shorten Urls
					signedURL, err := g.firebaseAdapter.GenerateSignedURL(BucketName, fmt.Sprintf("voicelines/%s", uuid.String()))
					if err != nil {
						g.logger.Error("error generating signed url", zap.Error(err), zap.String("member_created_for", member.User.ID), zap.String("member_created_by", interaction.Member.User.ID))
						return fmt.Errorf("error generating signed url %w", err)
					}

					g.Lock()
					urlsCreated = append(urlsCreated, signedURL)
					g.Unlock()

					return nil
				})
			}

			if err = eg.Wait(); err != nil || len(urlsCreated) == 0 {
				g.logger.Error("error creating or uploading files", zap.Error(err), zap.String("member_created_for", member.User.ID), zap.String("member_created_by", interaction.Member.User.ID))
				return err
			}

			successfulUploadEmbeds := embeds.SuccessfulAudioZipUploadEmbeds(member, interaction.Member, audioType, urlsCreated)

			if len(successfulUploadEmbeds) == 1 {
				_, err = session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Embeds: []*discordgo.MessageEmbed{
						successfulUploadEmbeds[0],
					},
				})
				if err != nil {
					g.logger.Error("error unable to send follow up embed: %v", zap.Error(err))
					return err
				}

				message, err := session.InteractionResponse(interaction.Interaction)
				if err != nil {
					return err
				}

				if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*2); err != nil {
					g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
				}
			} else {
				message, err := session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
					Components: embeds.GetPaginationComponent(true, true, false, false),
					Embeds:     []*discordgo.MessageEmbed{successfulUploadEmbeds[0]},
				})
				if err != nil {
					return fmt.Errorf("error sending pagination for upload command %w", err)
				}

				if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*2); err != nil {
					g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
				}

				g.messageStore[message.ID] = &paginationState{
					Pages:       successfulUploadEmbeds,
					CurrentPage: 0,
				}
			}

		default:
			message, err := session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{
					embeds.ErrorMessageEmbed("File must be an mp3 or m4a file!"),
				},
			})

			if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Second*10); err != nil {
				g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
			}

			if err != nil {
				g.logger.Error("unable to send follow up embed: %v", zap.Error(err))
				return err
			}
		}
	}

	return nil
}

func (g *greeterRunner) voicelines(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	options := interaction.ApplicationCommandData().Options
	memberID, audioType := options[0].Value.(string), options[1].Value.(string)

	member, err := session.State.Member(interaction.GuildID, memberID)
	if err != nil {
		g.logger.Error("error getting member to create audio track for", zap.Error(err), zap.String("user_id", memberID))
		return err
	}

	collectionName := OutroCollection
	audioListKey := OutroArrayKey

	if audioType == "intro" {
		audioListKey = IntroArrayKey
		collectionName = WelcomeCollection
	}

	ctx := context.Background()

	data, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collectionName, memberID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{embeds.NoDataForMemberEmbed(audioType, member.User.Username)},
				},
			})
			if err != nil {
				return err
			}

			message, err := session.InteractionResponse(interaction.Interaction)
			if err != nil {
				return err
			}

			if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Second*10); err != nil {
				g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
			}

			return err
		}

		g.logger.Error("error getting document from firestore", zap.Error(err), zap.String("member_id", memberID), zap.String("collection", collectionName))
		return err
	}

	if _, ok := data[audioListKey]; !ok {
		return errors.New("audio key not found in document")
	}

	urls := []string{}

	if data, ok := data[audioListKey].([]interface{}); ok {
		eg, _ := errgroup.WithContext(ctx)

		for _, trackRecord := range data {
			if track, ok := trackRecord.(map[string]interface{}); ok {
				eg.Go(func() error {
					trackTitle := track["track_name"].(string)

					signedUrl, err := g.firebaseAdapter.GenerateSignedURL(BucketName, fmt.Sprintf("voicelines/"+trackTitle))
					if err != nil {
						return err
					}

					g.Lock()
					urls = append(urls, signedUrl)
					g.Unlock()
					return nil
				})
			}
		}

		if err := eg.Wait(); err != nil {
			return fmt.Errorf("error retrieving generated signed urls %w", err)
		}
	} else {
		return errors.New("error could not cast record")
	}

	if data, ok := data[audioListKey].([]map[string]interface{}); ok {
		for _, trackRecord := range data[:4] {
			urls = append(urls, trackRecord["track_name"].(string))
		}
	}

	successEmbeds := embeds.GetSuccessfulAudioRetrievalEmbeds(member, audioType, urls)
	if len(successEmbeds) == 1 {
		err := session.InteractionRespond(interaction.Interaction,
			&discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{successEmbeds[0]},
				},
			})
		if err != nil {
			return err
		}

		message, err := session.InteractionResponse(interaction.Interaction)
		if err != nil {
			return err
		}

		if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*2); err != nil {
			g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
		}
	} else {
		err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Components: embeds.GetPaginationComponent(true, true, false, false),
				Embeds:     []*discordgo.MessageEmbed{successEmbeds[0]},
			},
		})
		if err != nil {
			return err
		}

		message, err := session.InteractionResponse(interaction.Interaction)
		if err != nil {
			return err
		}

		if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*2); err != nil {
			g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
		}

		g.messageStore[message.ID] = &paginationState{
			Pages:       successEmbeds,
			CurrentPage: 0,
		}
	}

	return nil
}

func (g *greeterRunner) messageComponentHandler(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	if interaction.Type != discordgo.InteractionMessageComponent {
		return
	}

	if state, exists := g.messageStore[interaction.Message.ID]; exists {
		switch interaction.MessageComponentData().CustomID {
		case "first":
			state.CurrentPage = 0
		case "last":
			state.CurrentPage = len(state.Pages) - 1
		case "prev":
			state.CurrentPage--
			if state.CurrentPage < 0 {
				state.CurrentPage = len(state.Pages) - 1
			}
		case "next":
			state.CurrentPage++
			if state.CurrentPage >= len(state.Pages) {
				state.CurrentPage = 0
			}
		}

		components := embeds.GetPaginationComponent(state.CurrentPage == 0, state.CurrentPage == 0, state.CurrentPage == len(state.Pages)-1, state.CurrentPage == len(state.Pages)-1)
		_, err := session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         interaction.Message.ID,
			Channel:    interaction.ChannelID,
			Embeds:     &[]*discordgo.MessageEmbed{state.Pages[state.CurrentPage]},
			Components: &components,
		})

		if err != nil {
			g.logger.Warn("error editing complex message", zap.Error(err))
		}

		if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
		}); err != nil {
			g.logger.Warn("error responding with update message response", zap.Error(err))
		}
	}
}

func (g *greeterRunner) help(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embeds.HelpMenuEmbed()},
		},
	})
	if err != nil {
		return err
	}

	message, err := session.InteractionResponse(interaction.Interaction)
	if err != nil {
		return err
	}

	if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*1); err != nil {
		g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
	}

	return err
}

func (g *greeterRunner) greeterHandler(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	if interaction.Type != discordgo.InteractionApplicationCommand {
		return
	}

	var err error

	switch interaction.ApplicationCommandData().Name {
	case "upload":
		err = g.upload(session, interaction)
	case "voicelines":
		err = g.voicelines(session, interaction)
	case "help":
		err = g.help(session, interaction)
	}

	if err != nil {
		g.logger.Error("An error occurred during when executing command", zap.Error(err), zap.String("command", interaction.ApplicationCommandData().Name))
		_ = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					embeds.UnexpectedErrorEmbed(),
				},
			},
		})
	}
}
