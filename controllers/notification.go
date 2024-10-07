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
	"errors"
	"fmt"
	"strings"

	goteamsnotify "github.com/atc0005/go-teams-notify/v2"
	"github.com/atc0005/go-teams-notify/v2/adaptivecard"
	"github.com/bwmarrin/discordgo"
	"github.com/go-logr/logr"
	webexteams "github.com/jbogarin/go-cisco-webex-teams/sdk"
	"github.com/slack-go/slack"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
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
	case libsveltosv1beta1.NotificationTypeSMTP:
		// TODO: add support for SMTP notifications
		err = errors.New("not suppoorted yet")
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

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	api := slack.New(info.token)
	if api == nil {
		l.V(logs.LogInfo).Info("failed to get slack client")
	}

	l.V(logs.LogDebug).Info(fmt.Sprintf("Sending message to channel %s", info.channelID))

	_, _, err = api.PostMessage(info.channelID, slack.MsgOptionText(message, false))
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

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

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

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)

	// Create a new Discord session using the provided token
	dg, err := discordgo.New("Bot " + info.token)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to get discord session")
		return err
	}

	// Create a new message with both a text content and the file attachment
	_, err = dg.ChannelMessageSendComplex(info.channelID, &discordgo.MessageSend{
		Content: message,
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

	message, _ := getNotificationMessage(clusterNamespace, clusterName, clusterType, conditions, logger)
	teamsClient := goteamsnotify.NewTeamsClient()

	// Validate Teams Webhook expected format
	if teamsClient.ValidateWebhook(info.webhookUrl) != nil {
		l.V(logs.LogInfo).Info("failed to validate Teams webhook URL: %v", err)
		return err
	}

	// Create adaptive card with the clusterName as the title of the message
	teamsMessage, err := adaptivecard.NewSimpleMessage(message, clusterName, true)
	if err != nil {
		l.V(logs.LogInfo).Info("failed to create Teams message: %v", err)
		return err
	}

	// Send the meesage with the user provided webhook URL
	if teamsClient.Send(info.webhookUrl, teamsMessage) != nil {
		l.V(logs.LogInfo).Info("failed to send Teams message: %v", err)
		return err
	}

	return err
}

func getNotificationMessage(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	conditions []libsveltosv1beta1.Condition, logger logr.Logger) (string, bool) {

	passing := true
	message := fmt.Sprintf("cluster %s:%s/%s  \n", clusterType, clusterNamespace, clusterName)
	for i := range conditions {
		c := &conditions[i]
		if c.Status != corev1.ConditionTrue {
			passing = false
			message += fmt.Sprintf("liveness check %q failing  \n", c.Type)
			message += fmt.Sprintf("%s  \n", c.Message)
		}
	}

	if passing {
		message += "all liveness checks are passing"
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
