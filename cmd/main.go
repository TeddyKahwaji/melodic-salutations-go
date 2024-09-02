package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	firebaseAdapter "salutations/internal/firebase"
	"salutations/internal/greeter"
	gcp "salutations/pkg/gcp"

	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go"
	"github.com/bwmarrin/discordgo"
	youtube "github.com/kkdai/youtube/v2"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

func getLogger(env string) *zap.Logger {
	if strings.ToUpper(env) == "PROD" {
		return zap.Must(zap.NewProduction())
	} else {
		return zap.Must(zap.NewDevelopment())
	}
}

func main() {
	const PROJECT_ID = "twitterbot-e7ab0"

	env := os.Getenv("ENV")

	logger := getLogger(env)

	defer func() {
		if err := logger.Sync(); err != nil {
			panic(fmt.Errorf("error syncing logger %w", err))
		}
	}()

	discordToken := os.Getenv("MELODY_DISCORD_TOKEN")
	httpClient := http.Client{
		Timeout: time.Second * 3,
	}

	bot, err := discordgo.New("Bot " + discordToken)
	bot.Client = &httpClient

	if err != nil {
		logger.Fatal("bot could not be booted", zap.Error(err))
	}

	bot.Identify.Intents = discordgo.IntentsAll
	bot.StateEnabled = true
	bot.Identify.Presence = discordgo.GatewayStatusUpdate{
		Game: discordgo.Activity{
			Name: "/help",
			Type: discordgo.ActivityTypeGame,
		},
	}
	bot.AddHandler(func(session *discordgo.Session, _ *discordgo.Ready) {
		ctx := context.Background()

		firebaseAdapter, err := NewFirebaseAdapter(ctx, PROJECT_ID, logger)
		if err != nil {
			panic(fmt.Sprintf("error instantiating firebase adapter: %v", err))
		}

		greeterCog, err := greeter.NewGreeterRunner(logger, &youtube.Client{}, firebaseAdapter)
		if err != nil {
			panic(fmt.Sprintf("unable to instantiate greeter cog, %v", err))
		}

		if err = greeterCog.RegisterCommands(session); err != nil {
			panic(fmt.Sprintf("error unable to register greeter commands: %v", err))
		}

		logger.Info("Bot has connected")
	})

	if err := bot.Open(); err != nil {
		logger.Fatal("error opening connection", zap.Error(err))
	}

	defer bot.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}

func NewFirebaseAdapter(ctx context.Context, projectID string, logger *zap.Logger) (firebaseAdapter.Firebase, error) {
	creds, err := gcp.GetCredentials()
	if err != nil {
		return nil, fmt.Errorf("error getting gcp credentials  %w", err)
	}

	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID}, option.WithCredentialsJSON(creds))
	if err != nil {
		return nil, fmt.Errorf("error creating new firebase client %w", err)
	}

	fsClient, err := app.Firestore(ctx)
	if err != nil {
		return nil, fmt.Errorf("error creating new firestore client %w", err)
	}

	storageClient, err := storage.NewClient(ctx, option.WithCredentialsJSON(creds))
	if err != nil {
		return nil, fmt.Errorf("error creating new storage client %w", err)
	}

	return firebaseAdapter.NewFirebaseHelper(fsClient, storageClient, logger), nil
}
