package greeter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	WelcomeCollection   string = "welcomeIntros"
	OutroCollection     string = "byeOutros"
	BlacklistCollection string = "blacklist"
	IntroArrayKey       string = "intro_array"
	OutroArrayKey       string = "outro_array"
	BucketName          string = "twitterbot-e7ab0.appspot.com"
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
	CurrentPage     int
	Pages           []*discordgo.MessageEmbed
	SelectMenuData  []string
	SelectMenuBound int
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

type trackData struct {
	TrackName      string
	TrackSignedURL string
}

type blacklistRecord struct {
	AddedOn time.Time `firestore:"added_on"`
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
			Name:        "delete",
			Description: "Delete voicelines for a given member",
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
		{
			Name:        "blacklist",
			Description: "Prevents bot from playing your intros and outros",
		},
		{
			Name:        "whitelist",
			Description: "This command removes you from the blacklist",
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
	hasLeft := vc.BeforeUpdate != nil && !vc.Member.User.Bot && vc.ChannelID == ""

	g.logger.Info("voice state update", zap.String("user_id", vc.Member.User.ID), zap.Bool("is_bot", vc.Member.User.Bot), zap.Bool("has_joined", hasJoined), zap.Bool("has_left", hasLeft))

	ctx := context.Background()
	isInBlacklist, err := g.isInBlacklist(ctx, vc.VoiceState.Member.User.ID)
	if err != nil {
		g.logger.Warn("unable to check blacklist status for user", zap.Error(err), zap.String("user_id", vc.VoiceState.Member.User.ID))
	}

	if isInBlacklist {
		return
	}

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
		channelMemberCount, err := util.GetVoiceChannelMemberCount(session, vc.BeforeUpdate.GuildID, vc.BeforeUpdate.ChannelID)
		if err != nil {
			g.logger.Error("error getting channel member count", zap.Error(err), zap.String("channel_id", vc.BeforeUpdate.ChannelID), zap.String("guild_id", vc.BeforeUpdate.GuildID))
			g.Unlock() // Unlock before returning
			return
		}

		// Only bot left in server
		if channelMemberCount == 1 {
			if botVoiceConnection, ok := session.VoiceConnections[vc.GuildID]; ok && botVoiceConnection.ChannelID == vc.BeforeUpdate.ChannelID {
				if err := botVoiceConnection.Disconnect(); err != nil {
					g.logger.Error("error disconnecting from channel", zap.Error(err), zap.String("channel_id", vc.BeforeUpdate.ChannelID))
					g.Unlock() // Unlock before returning
					return
				}

				delete(g.guildPlayerMappings, vc.GuildID)
			}
			g.Unlock()
			return
		}
		g.Unlock() // Unlock after modifications are done
	}

	var COLLECTION string
	if hasJoined {
		COLLECTION = WelcomeCollection
	} else {
		COLLECTION = OutroCollection
	}

	if hasJoined || hasLeft {
		var targetChannelID string
		if hasLeft {
			targetChannelID = vc.BeforeUpdate.ChannelID
		} else if hasJoined {
			targetChannelID = vc.ChannelID
		}
		g.Lock() // Lock before accessing shared data

		if _, ok := g.guildPlayerMappings[vc.GuildID]; !ok {
			perms, err := session.UserChannelPermissions(session.State.Ready.User.ID, targetChannelID)
			if err != nil {
				g.logger.Error("unable to get permissions for channel", zap.Error(err), zap.String("channel_id", targetChannelID))
				g.Unlock() // Unlock before returning
				return
			}

			if perms&int64(discordgo.PermissionVoiceConnect) == 0 || perms&int64(discordgo.PermissionVoiceSpeak) == 0 {
				g.logger.Info("Bot will not be joining voice channel because they do not have sufficient privileges", zap.String("channel_id", targetChannelID))
				g.Unlock() // Unlock before returning
				return
			}

			channelVoiceConnection, err := session.ChannelVoiceJoin(vc.GuildID, targetChannelID, false, true)
			if err != nil {
				g.logger.Error("error unable to join voice channel", zap.String("channel_id", targetChannelID), zap.String("guild_id", vc.GuildID), zap.Error(err))
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
			if status.Code(err) == codes.NotFound {
				g.logger.Info("voiceline won't be played because user does not have intro/outro", zap.String("user_id", vc.UserID))
			} else {
				g.logger.Error("failed to get random audio track from firestore", zap.Error(err), zap.String("user_id", vc.UserID))
			}
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

func (g *greeterRunner) retrieveTracks(ctx context.Context, collection string, userId string) ([]interface{}, error) {
	data, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, userId)
	if err != nil {
		return nil, fmt.Errorf("error getting document from collection: %w", err)
	}

	audioListKey := OutroArrayKey
	if collection == WelcomeCollection {
		audioListKey = IntroArrayKey
	}

	if audioArray, ok := data[audioListKey]; ok {
		if audioSlice, ok := audioArray.([]interface{}); ok {
			return audioSlice, nil
		}
	}

	return nil, fmt.Errorf("error key was not found: %w", err)
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
			g.logger.Warn("error trying to delete file", zap.Error(err), zap.String("file_name", audioPath))
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
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(guildPlayer.queue) > 0 {
					g.songSignal <- guildPlayer
				} else {
					guildPlayer.voiceState = NotPlaying
				}
			} else {
				g.logger.Error("error during audio stream", zap.Error(err))
			}
		} else {
			g.logger.Error("something went wrong during stream session", zap.Error(err))
		}
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
					g.logger.Warn("error closing body", zap.Error(err))
				}

				if err := util.DeleteFile(file.Name()); err != nil {
					g.logger.Warn("error trying to delete file", zap.Error(err), zap.String("file_name", file.Name()))
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

			if _, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, memberID); err != nil {
				if status.Code(err) == codes.NotFound {
					var err error
					if collection == WelcomeCollection {
						err = g.firebaseAdapter.CreateDocument(ctx, collection, memberID, firebaseIntroRecord{Name: memberID, IntroArray: []trackRecord{}})
					} else {
						err = g.firebaseAdapter.CreateDocument(ctx, collection, memberID, firebaseOutroRecord{Name: memberID, OutroArray: []trackRecord{}})
					}

					if err != nil {
						return fmt.Errorf("error creating firestore document: %w", err)
					}
				} else {
					return err
				}
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

			defer func() {
				if err := resp.Body.Close(); err != nil {
					g.logger.Warn("error closing body from request", zap.Error(err))
				}
			}()

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
							g.logger.Warn("error closing file", zap.Error(err))
						}

						if err := util.DeleteFile(f.Name()); err != nil {
							g.logger.Warn("error trying to delete file", zap.Error(err), zap.String("file_name", f.Name()))
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
				g.logger.Error("unable to send follow up embed", zap.Error(err))
				return err
			}
		}
	}

	return nil
}

func (g *greeterRunner) extractAudioTracksForUser(ctx context.Context, data map[string]interface{}, audioKey string) ([]trackData, error) {
	tracks := []trackData{}

	if data, ok := data[audioKey].([]interface{}); ok {
		eg, _ := errgroup.WithContext(ctx)
		for _, trackRecord := range data {
			if track, ok := trackRecord.(map[string]interface{}); ok {
				eg.Go(func() error {
					trackTitle := track["track_name"].(string)

					g.Lock()

					urlData, err := g.firebaseAdapter.GenerateSignedURL(BucketName, fmt.Sprintf("voicelines/"+trackTitle))
					if err != nil {
						return err
					}
					tracks = append(tracks, trackData{
						TrackName:      trackTitle,
						TrackSignedURL: urlData,
					})

					g.Unlock()
					return nil
				})
			}
		}

		if err := eg.Wait(); err != nil {
			return nil, fmt.Errorf("error retrieving generated signed urls %w", err)
		}
		return tracks, nil
	}

	return nil, errors.New("error could not cast record")
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

	trackData, err := g.extractAudioTracksForUser(ctx, data, audioListKey)
	if err != nil {
		return fmt.Errorf("unable to extract audio track for user: %v", err)
	}

	urls := make([]string, 0, len(trackData))
	for _, data := range trackData {
		urls = append(urls, data.TrackSignedURL)
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

	ctx := context.Background()

	var forwardButtonPressed bool

	var member *discordgo.Member

	var err error

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

			forwardButtonPressed = true
		default:
			componentData := strings.Split(interaction.MessageComponentData().CustomID, "|")
			memberID, collection := componentData[0], componentData[1]
			member, err = session.GuildMember(interaction.GuildID, memberID)
			if err != nil {
				g.logger.Warn("unable to update select menu component, could not get guild member", zap.Error(err), zap.String("user_id", memberID))
				return
			}

			valuesSelected := interaction.MessageComponentData().Values
			audioListKey := IntroArrayKey
			if collection == OutroCollection {
				audioListKey = OutroArrayKey
			}

			eg, ctx := errgroup.WithContext(ctx)
			tracks, err := g.retrieveTracks(ctx, collection, memberID)
			if err != nil {
				g.logger.Error("error retrieving users tracks", zap.Error(err), zap.String("user_id", memberID))
				return
			}

			for _, trackId := range valuesSelected {
				eg.Go(func() error {
					voicelineTrackPath := fmt.Sprintf("voicelines/%s", trackId)
					archiveTrackPath := fmt.Sprintf("archive/%s/%s", memberID, trackId)
					if err := g.firebaseAdapter.CloneFileFromStorage(ctx, BucketName, voicelineTrackPath, archiveTrackPath); err != nil {
						return err
					}

					if err := g.firebaseAdapter.DeleteFileFromStorage(ctx, BucketName, voicelineTrackPath); err != nil {
						return err
					}

					trackRecordToBeRemoved := make(map[string]interface{}, 3)

					for _, track := range tracks {
						if recordMap, ok := track.(map[string]interface{}); ok {
							if recordMap["track_name"].(string) == trackId {
								trackRecordToBeRemoved["track_name"] = recordMap["track_name"].(string)
								trackRecordToBeRemoved["added_by"] = recordMap["added_by"].(string)
								if time, ok := recordMap["created_at"].(time.Time); ok {
									trackRecordToBeRemoved["created_at"] = time
								}
								break
							}
						}
					}
					data := map[string]interface{}{
						audioListKey: firestore.ArrayRemove(trackRecordToBeRemoved),
					}

					if err := g.firebaseAdapter.UpdateDocument(ctx, collection, memberID, data); err != nil {
						return err
					}
					return nil
				})
			}

			if err := eg.Wait(); err != nil {
				g.logger.Error("error deleting voicelines for user", zap.Error(err), zap.String("collection", collection), zap.String("user_id", memberID))
				return
			}

			message, err := session.ChannelMessageEditComplex(&discordgo.MessageEdit{
				ID:         interaction.Message.ID,
				Channel:    interaction.ChannelID,
				Components: &[]discordgo.MessageComponent{},
				Embeds:     &[]*discordgo.MessageEmbed{embeds.DeleteCompletedSuccessEmbed(len(valuesSelected), member, interaction.Member)},
			})
			if err != nil {
				g.logger.Warn("error editing complex message", zap.Error(err))
				return
			}

			if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Second*30); err != nil {
				g.logger.Warn("unable to delete message")
			}

			return
		}

		message, err := session.ChannelMessage(interaction.ChannelID, interaction.Message.ID)
		if err != nil {
			g.logger.Warn("error retrieving channel message in component handler", zap.Error(err))
			return
		}

		buttonStatusMapping := map[int]bool{
			0: state.CurrentPage == 0,
			1: state.CurrentPage == 0,
			2: state.CurrentPage == len(state.Pages)-1,
			3: state.CurrentPage == len(state.Pages)-1,
		}

		// Update button disabled status
		if buttonsActionRow, ok := message.Components[0].(*discordgo.ActionsRow); ok {
			for buttonIndex, buttonDisabled := range buttonStatusMapping {
				if buttonData, ok := buttonsActionRow.Components[buttonIndex].(*discordgo.Button); ok {
					buttonData.Disabled = buttonDisabled
					buttonsActionRow.Components[buttonIndex] = buttonData
				}
			}
			message.Components[0] = buttonsActionRow
		}

		// Update select menu data
		if state.SelectMenuData != nil {
			if selectMenuActionRow, ok := message.Components[1].(*discordgo.ActionsRow); ok {
				if selectMenu, ok := selectMenuActionRow.Components[0].(*discordgo.SelectMenu); ok {
					componentData := strings.Split(selectMenu.CustomID, "|")
					memberID, _ := componentData[0], componentData[1]
					member, err = session.GuildMember(interaction.GuildID, memberID)
					if err != nil {
						g.logger.Warn("unable to update select menu component, could not get guild member", zap.Error(err), zap.String("user_id", memberID))
						return
					}
					options := []discordgo.SelectMenuOption{}

					var minBound, maxBound int

					totalItems := len(state.SelectMenuData)
					itemsPerPage := 4 // maximum items per page

					if state.CurrentPage == 0 {
						// First page: Show the first set of items
						minBound = 0
						maxBound = min(itemsPerPage, totalItems)
					} else if state.CurrentPage == len(state.Pages)-1 {
						// Last page: Show the last set of items
						minBound = max(0, len(state.SelectMenuData)-len(state.Pages[state.CurrentPage].Fields))
						maxBound = len(state.SelectMenuData)
					} else if forwardButtonPressed {
						// Moving forward: Adjust bounds based on the current bounds
						minBound = state.SelectMenuBound
						maxBound = min(state.SelectMenuBound+itemsPerPage, totalItems)
					} else {
						fieldsInPageAfter := len(state.Pages[state.CurrentPage+1].Fields)
						maxBound = state.SelectMenuBound - fieldsInPageAfter
						minBound = maxBound - 4
					}

					// Ensure maxBound is not greater than the total number of items
					if maxBound > totalItems {
						maxBound = totalItems
					}

					for i := minBound; i < maxBound; i++ {
						options = append(options, discordgo.SelectMenuOption{Label: fmt.Sprintf("%s's Voiceline %d", member.User.Username, i+1), Value: state.SelectMenuData[i]})
					}
					selectMenu.MaxValues = len(options)
					selectMenu.Options = options
					state.SelectMenuBound = maxBound
					selectMenuActionRow.Components[0] = selectMenu
				}
				message.Components[1] = selectMenuActionRow
			}
		}

		_, err = session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID:         interaction.Message.ID,
			Channel:    interaction.ChannelID,
			Embeds:     &[]*discordgo.MessageEmbed{state.Pages[state.CurrentPage]},
			Components: &message.Components,
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
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		return err
	}

	return nil
}

func (g *greeterRunner) isInBlacklist(ctx context.Context, memberId string) (bool, error) {
	_, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, BlacklistCollection, memberId)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (g *greeterRunner) blacklist(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	ctx := context.Background()
	isInBlacklist, err := g.isInBlacklist(ctx, interaction.Member.User.ID)
	if err != nil {
		return fmt.Errorf("error attempting to check if user is already in blacklist %w", err)
	}

	if isInBlacklist {
		err = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embeds.AlreadyOnBlacklistEmbed(interaction.Member)},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			return fmt.Errorf("error attempting to send already on blacklist embed message: %w", err)
		}
		return nil
	}

	err = g.firebaseAdapter.CreateDocument(ctx, BlacklistCollection, interaction.Member.User.ID, &blacklistRecord{
		AddedOn: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("error attempting to create firebase document containing blacklist information: %w", err)
	}

	err = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embeds.AddedToBlacklistEmbed(interaction.Member)},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		return fmt.Errorf("error attempting to send response: %w", err)
	}

	return nil
}

func (g *greeterRunner) whitelist(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	ctx := context.Background()
	isInBlacklist, err := g.isInBlacklist(ctx, interaction.Member.User.ID)
	if err != nil {
		return fmt.Errorf("error attempting to check if user is in blacklist: %w", err)
	}

	if !isInBlacklist {
		err = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embeds.NotOnBlacklistEmbed(interaction.Member)},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			return fmt.Errorf("error attempting to send response: %w", err)
		}

		return nil
	}

	err = g.firebaseAdapter.DeleteDocument(ctx, BlacklistCollection, interaction.Member.User.ID)
	if err != nil {
		return fmt.Errorf("error deleting document: %w", err)
	}

	err = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embeds.RemovedFromBlacklistEmbed(interaction.Member)},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		return fmt.Errorf("error attempting to send response: %w", err)
	}

	return nil
}

func (g *greeterRunner) delete(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
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
		g.logger.Error("error getting member data", zap.Error(err), zap.String("user_id", memberID))
		return err
	}

	ctx := context.Background()
	collection := WelcomeCollection
	audioKey := IntroArrayKey

	if audioType == "outro" {
		collection = OutroCollection
		audioKey = OutroArrayKey
	}

	data, err := g.firebaseAdapter.GetDocumentFromCollection(ctx, collection, memberID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			message, err := session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{embeds.NoDataForMemberEmbed(audioType, member.User.Username)},
			})
			if err != nil {
				return fmt.Errorf("error sending follow up message that no data exists for user: %w", err)
			}

			if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*1); err != nil {
				g.logger.Warn("failed to delete message with delay", zap.Error(err))
			}

			return nil
		}

		return fmt.Errorf("error unable to get document from collection: %w", err)
	}

	trackData, err := g.extractAudioTracksForUser(ctx, data, audioKey)
	if err != nil {
		return fmt.Errorf("error extracting audio track for user: %w", err)
	}

	trackNames := make([]string, 0, len(trackData))
	urls := make([]string, 0, len(trackData))
	for _, data := range trackData {
		trackNames = append(trackNames, data.TrackName)
		urls = append(urls, data.TrackSignedURL)
	}

	successEmbeds := embeds.GetSuccessfulAudioRetrievalEmbeds(member, audioType, urls)
	paginationComponent := embeds.GetPaginationComponent(false, true, false, false)

	menuOptions := make(map[string]string)
	menuBound := min(len(trackNames), 4)
	for i, trackName := range trackNames[:menuBound] {
		menuOptions[fmt.Sprintf("%s's voiceline %d", member.User.Username, i+1)] = trackName
	}

	paginationComponent, err = embeds.AddSelectMenu(paginationComponent, memberID, collection, menuOptions)
	if err != nil {
		return fmt.Errorf("error adding select menu: %w", err)
	}

	var message *discordgo.Message
	if len(successEmbeds) == 1 {
		components, err := embeds.AddSelectMenu([]discordgo.MessageComponent{}, memberID, collection, menuOptions)
		if err != nil {
			return fmt.Errorf("error adding select menu to single page delete menu: %w", err)
		}

		message, err = session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
			Embeds:     []*discordgo.MessageEmbed{successEmbeds[0]},
			Components: components,
		})
		if err != nil {
			return fmt.Errorf("error sending follow up delete message: %w", err)
		}
	} else {
		message, err = session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
			Components: paginationComponent,
			Embeds:     []*discordgo.MessageEmbed{successEmbeds[0]},
		})
		if err != nil {
			return fmt.Errorf("error sending paginated delete menu: %w", err)
		}
	}

	g.messageStore[message.ID] = &paginationState{
		Pages:           successEmbeds,
		CurrentPage:     0,
		SelectMenuData:  trackNames,
		SelectMenuBound: menuBound,
	}
	if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Minute*2); err != nil {
		g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
	}

	return nil
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
	case "blacklist":
		err = g.blacklist(session, interaction)
	case "whitelist":
		err = g.whitelist(session, interaction)
	case "delete":
		err = g.delete(session, interaction)
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

		message, err := session.InteractionResponse(interaction.Interaction)
		if err != nil {
			g.logger.Error("An error attempting to retrieve interaction response", zap.Error(err))
		} else if err := util.DeleteMessageAfterTime(session, interaction.ChannelID, message.ID, time.Second*30); err != nil {
			g.logger.Warn("failed to delete message with delay", zap.Error(err), zap.String("message_id", message.ID))
		}

	}
}
