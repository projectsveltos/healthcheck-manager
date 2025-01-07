/*
Copyright 2023. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	goteamsnotify "github.com/atc0005/go-teams-notify/v2"
	"github.com/atc0005/go-teams-notify/v2/adaptivecard"
	"github.com/bwmarrin/discordgo"
	"github.com/go-logr/logr"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	webexteams "github.com/jbogarin/go-cisco-webex-teams/sdk"
	"github.com/slack-go/slack"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	sveltosnotifications "github.com/projectsveltos/libsveltos/lib/notifications"
)

type slackInfo struct {
	token     string
	channelID string
}

type webexInfo struct {
	token string
	room  string
}

type discordInfo struct {
	token     string
	channelID string
}

type teamsInfo struct {
	webhookUrl string
}

type telegramInfo struct {
	token  string
	chatID int64
}

// sendNotification delivers notification
func sendNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition, logger logr.Logger) error {

	logger = logger.WithValues("notification", fmt.Sprintf("%s:%s", n.Type, n.Name))
	logger.V(logs.LogDebug).Info("deliver notification")

	var err error
	switch n.Type {
	case libsveltosv1beta1.NotificationTypeKubernetesEvent:
		sendKubernetesNotification(clusterNamespace, clusterName, clusterType, chc, conditions, logger)
	case libsveltosv1beta1.NotificationTypeSlack:
		err = sendSlackNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	case libsveltosv1beta1.NotificationTypeWebex:
		err = sendWebexNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	case libsveltosv1beta1.NotificationTypeDiscord:
		err = sendDiscordNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	case libsveltosv1beta1.NotificationTypeTeams:
		err = sendTeamsNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	case libsveltosv1beta1.NotificationTypeTelegram:
		err = sendTelegramNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	case libsveltosv1beta1.NotificationTypeSMTP:
		err = sendSMTPNotification(ctx, c, clusterNamespace, clusterName, clusterType, n, conditions, logger)
	default:
		logger.V(logs.LogInfo).Info("no handler registered for notification")
		panic(1)
	}

	if err != nil {
		logger.V(logs.LogInfo).Info("failed to send notification")
		return err
	}
	logger.V(logs.LogDebug).Info("notification delivered")
	return nil
}

func sendKubernetesNotification(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck,
	conditions []libsveltosv1beta1.Condition, logger logr.Logger) {

	message, passing := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	eventType := corev1.EventTypeNormal
	if !passing {
		eventType = corev1.EventTypeWarning
	}

	r := getManagementRecorder()
	r.Eventf(chc, eventType, "ClusterHealthCheck", message)

	r.Event(chc, eventType, "ClusterHealthCheck", message)
}

func sendSlackNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	info, err := getSlackInfo(ctx, c, n)
	if err != nil {
		return err
	}

	l := logger.WithValues("channel", info.channelID)
	l.V(logs.LogInfo).Info("send slack message")

	message, passing := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	msgSlack, err := composeSlackMessage(message, passing)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to format slack message: %v", err)
		return err
	}

	api := slack.New(info.token)
	if api == nil {
		l.V(logs.LogInfo).Info("failed to get slack client")
	}

	l.V(logs.LogDebug).Info(fmt.Sprintf("Sending message to channel %s", info.channelID))

	_, _, err = api.PostMessage(info.channelID, slack.MsgOptionText("ProjectSveltos Updates", false),
		slack.MsgOptionAttachments(msgSlack))
	if err != nil {
		l.V(logs.LogInfo).Info(fmt.Sprintf("Failed to send message. Error: %v", err))
		return err
	}

	return nil
}

func sendWebexNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	info, err := getWebexInfo(ctx, c, n)
	if err != nil {
		return err
	}

	message, passing := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	formattedMessage, err := composeWebexMessage(message, passing, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to format webex message: %v", err)
		return err
	}

	webexClient := webexteams.NewClient()
	if webexClient == nil {
		logger.V(logs.LogInfo).Info("failed to get webexClient client")
		return fmt.Errorf("failed to get webexClient client")
	}
	webexClient.SetAuthToken(info.token)

	l := logger.WithValues("channel", info.room)
	l.V(logs.LogInfo).Info("send webex message")

	message = strings.ReplaceAll(message, "\n", "  \n")

	webexMessage := &webexteams.MessageCreateRequest{
		RoomID:   info.room,
		Markdown: message,
		Attachments: []webexteams.Attachment{{
			ContentType: webexContentType,
			Content:     formattedMessage,
		}},
	}

	_, resp, err := webexClient.Messages.CreateMessage(webexMessage)
	if err != nil {
		l.V(logs.LogInfo).Info(fmt.Sprintf("Failed to send message. Error: %v", err))
		return err
	}

	if resp != nil {
		l.V(logs.LogDebug).Info(fmt.Sprintf("response: %s", string(resp.Body())))
	}

	return nil
}

func sendDiscordNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	info, err := getDiscordInfo(ctx, c, n)
	if err != nil {
		return err
	}

	l := logger.WithValues("channel", info.channelID)
	l.V(logs.LogInfo).Info("send discord message")

	message, passing := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	// Format Message
	discordReply, err := composeDiscordMessage(message, passing)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to format discord message: %v", err)
		return err
	}

	// Create a new Discord session using the provided token
	dg, err := discordgo.New("Bot " + info.token)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to get discord session")
		return err
	}

	// Send message with formatted message in embeds
	_, err = dg.ChannelMessageSendComplex(info.channelID, &discordgo.MessageSend{
		Content: "ProjectSveltos Updates",
		Embeds:  discordReply,
	})

	return err
}

func sendTeamsNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	info, err := getTeamsInfo(ctx, c, n)
	if err != nil {
		return err
	}

	l := logger.WithValues("webhookUrl", info.webhookUrl)
	l.V(logs.LogInfo).Info("send teams message")

	message, passing := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	// Format message using adaptive cards
	card, err := composeTeamsMessage(message, passing, logger)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to format teams message: %v", err)
		return err
	}

	// Create message and attach card
	teamsMessage := &adaptivecard.Message{Type: adaptivecard.TypeMessage}
	if err := teamsMessage.Attach(card); err != nil {
		l.V(logs.LogInfo).Info("failed to add teams card: %v", err)
		return err
	}

	// Prepare message
	if err := teamsMessage.Prepare(); err != nil {
		l.V(logs.LogInfo).Info("failed to prepare teams message payload: %v", err)
		return err
	}

	teamsClient := goteamsnotify.NewTeamsClient()

	// Validate Teams Webhook expected format
	if teamsClient.ValidateWebhook(info.webhookUrl) != nil {
		l.V(logs.LogInfo).Info("failed to validate Teams webhook URL: %v", err)
		return err
	}

	// Send the message with the user provided webhook URL
	if teamsClient.Send(info.webhookUrl, teamsMessage) != nil {
		l.V(logs.LogInfo).Info("failed to send Teams message: %v", err)
		return err
	}

	return err
}

func sendTelegramNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	info, err := getTelegramInfo(ctx, c, n)
	if err != nil {
		return err
	}

	l := logger.WithValues("chatid", info.chatID)
	l.V(logs.LogInfo).Info("send telegram message")

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	bot, err := tgbotapi.NewBotAPI(info.token)
	if err != nil {
		l.V(logs.LogInfo).Info(fmt.Sprintf("failed to get telegram bot: %v", err))
		return err
	}

	l.V(logs.LogDebug).Info("sending message to channel")

	msg := tgbotapi.NewMessage(info.chatID, message)
	_, err = bot.Send(msg)

	return err
}

func sendSMTPNotification(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, n *libsveltosv1beta1.Notification, conditions []libsveltosv1beta1.Condition,
	logger logr.Logger) error {

	mailer, err := sveltosnotifications.NewMailer(ctx, c, n)
	if err != nil {
		return err
	}

	l := logger.WithValues("notification", n.Name)
	l.V(logs.LogInfo).Info("send smtp message")

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	return mailer.SendMail("Sveltos Notification", message, false, nil)
}

func getNotificationMessage(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	conditions []libsveltosv1beta1.Condition, logger logr.Logger) (string, bool) {

	passing := true
	message := fmt.Sprintf("Cluster %s:%s/%s  \n", clusterType, clusterNamespace, clusterName)
	for i := range conditions {
		c := &conditions[i]
		if c.Status != corev1.ConditionTrue {
			passing = false
			message += fmt.Sprintf("Liveness check %q failing  \n", c.Type)
			message += fmt.Sprintf("%s  \n", c.Message)
		}
	}

	if passing {
		message += "All liveness checks are passing"
		logger.V(logs.LogDebug).Info("all liveness checks are passing")
	} else {
		logger.V(logs.LogDebug).Info("some of the liveness checks are not passing")
	}

	return message, passing
}

// buildNotificationStatusMap creates a map reporting notification status by walking over ClusterHealthCheck status
func buildNotificationStatusMap(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, chc *libsveltosv1beta1.ClusterHealthCheck) map[string]libsveltosv1beta1.NotificationStatus {

	notificationStatus := make(map[string]libsveltosv1beta1.NotificationStatus)

	for i := range chc.Status.ClusterConditions {
		cc := &chc.Status.ClusterConditions[i]
		if isClusterConditionForCluster(cc, clusterNamespace, clusterName, clusterType) {
			for j := range cc.NotificationSummaries {
				n := &cc.NotificationSummaries[j]
				notificationStatus[n.Name] = n.Status
			}
		}
	}

	return notificationStatus
}

// doSendNotification returns true if notification needs to be delivered, which happens when either of the following are true:
// - resendAll is true
// - there is no entry in notificationStatus (which means this notification was never delivered)
// - there is an entry in notificationStatus but the status is not set to delivered
func doSendNotification(n *libsveltosv1beta1.Notification, notificationStatus map[string]libsveltosv1beta1.NotificationStatus,
	resendAll bool) bool {

	if resendAll {
		return true
	}

	status, ok := notificationStatus[n.Name]
	if !ok {
		return true
	}

	return status != libsveltosv1beta1.NotificationStatusDelivered
}

func getSlackInfo(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*slackInfo, error) {
	secret, err := getSecret(ctx, c, n)
	if err != nil {
		return nil, err
	}

	authToken, ok := secret.Data[libsveltosv1beta1.SlackToken]
	if !ok {
		return nil, fmt.Errorf("secret does not contain slack token")
	}

	channelID, ok := secret.Data[libsveltosv1beta1.SlackChannelID]
	if !ok {
		return nil, fmt.Errorf("secret does not contain slack channelID")
	}

	return &slackInfo{token: string(authToken), channelID: string(channelID)}, nil
}

func getWebexInfo(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*webexInfo, error) {
	secret, err := getSecret(ctx, c, n)
	if err != nil {
		return nil, err
	}

	authToken, ok := secret.Data[libsveltosv1beta1.WebexToken]
	if !ok {
		return nil, fmt.Errorf("secret does not contain webex token")
	}

	room, ok := secret.Data[libsveltosv1beta1.WebexRoomID]
	if !ok {
		return nil, fmt.Errorf("secret does not contain webex room")
	}

	return &webexInfo{token: string(authToken), room: string(room)}, nil
}

func getDiscordInfo(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*discordInfo, error) {
	secret, err := getSecret(ctx, c, n)
	if err != nil {
		return nil, err
	}

	authToken, ok := secret.Data[libsveltosv1beta1.DiscordToken]
	if !ok {
		return nil, fmt.Errorf("secret does not contain discord token")
	}

	channelID, ok := secret.Data[libsveltosv1beta1.DiscordChannelID]
	if !ok {
		return nil, fmt.Errorf("secret does not contain discord channel id")
	}

	return &discordInfo{token: string(authToken), channelID: string(channelID)}, nil
}

func getSecret(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*corev1.Secret, error) {
	if n.NotificationRef == nil {
		return nil, fmt.Errorf("notification must reference secret containing slack token/channel id")
	}

	if n.NotificationRef.Kind != "Secret" {
		return nil, fmt.Errorf("notification must reference secret containing slack token/channel id")
	}

	if n.NotificationRef.APIVersion != "v1" {
		return nil, fmt.Errorf("notification must reference secret containing slack token/channel id")
	}

	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: n.NotificationRef.Namespace,
		Name:      n.NotificationRef.Name,
	}, secret)
	if err != nil {
		return nil, err
	}

	if secret.Type != libsveltosv1beta1.ClusterProfileSecretType {
		return nil, fmt.Errorf("referenced secret must be of type %q", libsveltosv1beta1.ClusterProfileSecretType)
	}

	if secret.Data == nil {
		return nil, fmt.Errorf("notification must reference secret containing slack token/channel id")
	}

	return secret, nil
}

func getTeamsInfo(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*teamsInfo, error) {
	secret, err := getSecret(ctx, c, n)
	if err != nil {
		return nil, err
	}

	webhookUrl, ok := secret.Data[libsveltosv1beta1.TeamsWebhookURL]
	if !ok {
		return nil, fmt.Errorf("secret does not contain webhook URL")
	}

	return &teamsInfo{webhookUrl: string(webhookUrl)}, nil
}

func getTelegramInfo(ctx context.Context, c client.Client, n *libsveltosv1beta1.Notification) (*telegramInfo, error) {
	secret, err := getSecret(ctx, c, n)
	if err != nil {
		return nil, err
	}

	authToken, ok := secret.Data[libsveltosv1beta1.TelegramToken]
	if !ok {
		return nil, fmt.Errorf("secret does not contain telegram token")
	}

	chatIDData, ok := secret.Data[libsveltosv1beta1.TelegramChatID]
	if !ok {
		return nil, fmt.Errorf("secret does not contain telegram chatID")
	}

	str := string(chatIDData)
	chatID, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to get chatID")
	}

	return &telegramInfo{token: string(authToken), chatID: chatID}, nil
}

func composeDiscordMessage(message string, passing bool) ([]*discordgo.MessageEmbed, error) {
	lines := strings.Split(message, "\n")
	if len(lines) == 0 {
		embed := []*discordgo.MessageEmbed{{
			Type:  discordgo.EmbedTypeRich,
			Title: "no message",
		}}
		return embed, fmt.Errorf("empty message")
	}

	description := "Failing some checks."
	color := discordRed
	if passing {
		description = "Passing!"
		color = discordGreen
	}

	content := []*discordgo.MessageEmbedField{}
	title := lines[0]
	name := ""
	val := ""
	empty := true

	fail_reg := regexp.MustCompile(failedTestRegexp)

	for _, line := range lines[1:] {
		if fail_reg.MatchString(line) {
			if name != "" {
				content = append(content, &discordgo.MessageEmbedField{Name: name, Value: val})
				empty = true
			}
			name = line
		} else if empty {
			val = line
			empty = false
		} else {
			val += "\n" + line
		}
	}
	content = append(content, &discordgo.MessageEmbedField{Name: name, Value: val})

	embed := []*discordgo.MessageEmbed{{
		Type:        discordgo.EmbedTypeRich,
		Title:       title,
		Description: description,
		Color:       color,
		Fields:      content,
	}}
	return embed, nil
}

func composeTeamsMessage(message string, passing bool, logger logr.Logger) (adaptivecard.Card, error) {
	card := adaptivecard.NewCard()

	lines := strings.Split(message, "\n")
	if len(lines) == 0 {
		return card, fmt.Errorf("empty message")
	}

	titleBlock := adaptivecard.NewTextBlock(lines[0], true)
	titleBlock.Weight = adaptivecard.WeightBolder
	titleBlock.Size = adaptivecard.SizeLarge

	// Adding title to card
	if err := card.AddElement(false, titleBlock); err != nil {
		logger.V(logs.LogDebug).Info("error adding card")
	}

	if passing {
		titleBlock.Text = "Passing! \n"
		titleBlock.Color = adaptivecard.ColorGood
	} else {
		titleBlock.Text = "Failing some checks. \n"
		titleBlock.Color = adaptivecard.ColorAttention
	}

	// Adding msg summary -- all tests passing or some failing
	if err := card.AddElement(false, titleBlock); err != nil {
		logger.V(logs.LogDebug).Info("error adding card")
	}

	// Adding remaining msg to card
	fail_regex := regexp.MustCompile(failedTestRegexp)

	for _, line := range lines[1:] {
		textblock := adaptivecard.NewTextBlock(line, true)

		if fail_regex.MatchString(line) {
			textblock.Color = adaptivecard.ColorAttention
			textblock.Separator = true
		} else {
			textblock.Separator = false
			textblock.Color = adaptivecard.ColorDefault
		}

		if err := card.AddElement(false, textblock); err != nil {
			logger.V(logs.LogDebug).Info("error adding card")
		}
	}
	return card, nil
}

func composeWebexMessage(message string, passing bool, logger logr.Logger) (map[string]interface{}, error) {
	cardData := map[string]interface{}{}

	// using addaptivecard from teams formatting
	card, err := composeTeamsMessage(message, passing, logger)
	if err != nil {
		return cardData, err
	}

	// fixing version/schema for webex compatibility
	card.Version = webexAdaptiveCardVersion
	card.Schema = webexAdaptiveCardSchema

	// convert adaptiveCard to map[string]interface{}
	jsonBytes, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		logger.V(logs.LogDebug).Info("Error Marshaling Card")
		return cardData, err
	}

	err = json.Unmarshal(jsonBytes, &cardData)
	if err != nil {
		logger.V(logs.LogDebug).Info("Error Unmarshaling Card")
		return cardData, err
	}

	// Remove added field for teams adaptive card : not required for webex
	delete(cardData, webexCardFieldMSTeams)

	return cardData, nil
}

func composeSlackMessage(message string, passing bool) (slack.Attachment, error) {
	attachment := slack.Attachment{
		MarkdownIn: []string{"text"},
	}

	lines := strings.Split(message, "\n")
	if len(lines) == 0 {
		return attachment, fmt.Errorf("empty message")
	}

	// adding color to summarize msg
	color := slackRed
	if passing {
		color = slackGreen
	}
	attachment.Color = color

	// adding title
	attachment.Title = lines[0]

	// adding the remaining msg
	markdownText := strings.Builder{}
	fail_reg := regexp.MustCompile(failedTestRegexp)

	for _, line := range lines[1:] {
		if fail_reg.MatchString(line) {
			markdownText.WriteString("*" + line + "*\n")
		} else {
			markdownText.WriteString(line + "\n")
		}
	}
	attachment.Text = markdownText.String()
	return attachment, nil
}
