package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"salutations/pkg/greeter"
	"strings"

	firebaseAdapter "salutations/internal/firebase"
	gcp "salutations/pkg/gcp"

	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go"
	"github.com/bwmarrin/discordgo"
	"github.com/kkdai/youtube/v2"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

const PROJECT_ID = "twitterbot-e7ab0"

var bot *discordgo.Session
var logger *zap.Logger

func main() {
	env := os.Getenv("ENV")
	if strings.ToUpper(env) == "PROD" {
		logger = zap.Must(zap.NewProduction())
	} else {
		logger = zap.Must(zap.NewDevelopment())
	}

	defer logger.Sync()

	discordToken := os.Getenv("MELODY_DISCORD_TOKEN")
	bot, err := discordgo.New(fmt.Sprintf("Bot %s", discordToken))
	if err != nil {
		logger.Fatal("bot could not be booted", zap.Error(err))
	}
	bot.AddHandler(onReady)
	if err := bot.Open(); err != nil {
		logger.Fatal("error opening connection", zap.Error(err))
	}

	defer bot.Close()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}

func NewFirebaseAdapter(ctx context.Context, projectId string) (firebaseAdapter.Firebase, error) {
	creds, err := gcp.GetCredentials()
	if err != nil {
		return nil, err
	}
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectId}, option.WithCredentialsJSON(creds))
	if err != nil {
		return nil, err
	}
	fsClient, err := app.Firestore(ctx)
	if err != nil {
		return nil, err
	}

	storageClient, err := storage.NewClient(ctx, option.WithCredentialsJSON(creds))
	if err != nil {
		return nil, err
	}

	return firebaseAdapter.NewFirebaseHelper(fsClient, storageClient), nil
}

func onReady(s *discordgo.Session, _ *discordgo.Ready) {
	ctx := context.Background()
	firebaseAdapter, err := NewFirebaseAdapter(ctx, PROJECT_ID)
	if err != nil {
		panic(fmt.Sprintf("error instantiating firebase adapter: %v", err))
	}
	greeterCog, err := greeter.NewGreeterRunner(logger, &youtube.Client{}, firebaseAdapter)
	if err != nil {
		panic(fmt.Sprintf("unable to instantiate greeter cog, %v", err))
	}
	if err = greeterCog.RegisterCommands(s); err != nil {
		panic(fmt.Sprintf("error unable to register greeter commands: %v", err))
	}

	logger.Info("Bot has connected")
}
