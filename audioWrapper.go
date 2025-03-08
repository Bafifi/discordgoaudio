package discordgoaudio

import (
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type AudioState struct {
	mutex      sync.Mutex
	Player     *audioPlayer
	lastPlayed struct {
		lastPlayedTime        time.Time
		isPlaying             bool
		shouldCheckDisconnect bool
	}
}

var serverAudioState = make(map[string]*AudioState)
var stateMutex sync.RWMutex

// GetServerAudioState returns the AudioState for a given guild ID.
// If the AudioState doesn't exist, it creates a new one.
func GetServerAudioState(guildID string) *AudioState {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	if state, exists := serverAudioState[guildID]; exists {
		return state
	}

	newState := &AudioState{
		Player: createNewAudioPlayer(),
	}
	serverAudioState[guildID] = newState
	return newState
}

func ResetAudioState(guildID string) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	if state, exists := serverAudioState[guildID]; exists {
		state.Player.StopAudioChannel <- true
		state.Player = createNewAudioPlayer()
		state.lastPlayed.isPlaying = false
		state.lastPlayed.shouldCheckDisconnect = false
	}
}

// LeaveVoice disconnects from the voice channel in the specified guild.
func LeaveVoice(s *discordgo.Session, guildID string) error {
	if vc, exists := s.VoiceConnections[guildID]; exists {
		if err := vc.Disconnect(); err != nil {
			return fmt.Errorf("error disconnecting from voice channel: %w", err)
		}
		vc.Close()
	}
	return nil
}

// FindUserVoiceChannel finds the voice channel ID for a given user in any guild.
func FindUserVoiceChannel(s *discordgo.Session, userID string) (string, error) {
	for _, guild := range s.State.Guilds {
		for _, voiceState := range guild.VoiceStates {
			if voiceState.UserID == userID {
				return voiceState.ChannelID, nil
			}
		}
	}
	return "", fmt.Errorf("user is not in a voice channel")
}

// JoinChannel joins a voice channel and plays audio from the specified path.
func JoinChannel(s *discordgo.Session, guildID, userID, path string) error {
	audioState := GetServerAudioState(guildID)
	audioState.mutex.Lock()
	defer audioState.mutex.Unlock()

	return runJoinChannel(s, audioState, guildID, userID, path, 0)
}

func runJoinChannel(s *discordgo.Session, audioState *AudioState, guildID, userID, path string, tries int) error {
	voiceChannelID, err := FindUserVoiceChannel(s, userID)
	if err != nil {
		return fmt.Errorf("error finding user voice channel: %w", err)
	}

	time.Sleep(350 * time.Millisecond)

	voiceConnection, err := s.ChannelVoiceJoin(guildID, voiceChannelID, false, true)
	if err != nil {
		if tries < 10 {
			return runJoinChannel(s, audioState, guildID, userID, path, tries+1)
		}
		return fmt.Errorf("error joining voice channel after %d tries: %w", tries, err)
	}

	go func() {
		for {
			time.Sleep(5 * time.Second)
			if voiceConnection == nil || !voiceConnection.Ready {
				ResetAudioState(guildID)
				return
			}
		}
	}()

	if err := voiceConnection.Speaking(true); err != nil {
		if leaveErr := LeaveVoice(s, guildID); leaveErr != nil {
			return fmt.Errorf("error setting speaking state: %v, and error leaving voice: %w", err, leaveErr)
		}
		return fmt.Errorf("error setting speaking state: %w", err)
	}

	audioState.lastPlayed.isPlaying = true
	audioState.lastPlayed.shouldCheckDisconnect = true

	defer func() {
		time.Sleep(1 * time.Second)

		audioState.lastPlayed.lastPlayedTime = time.Now()
		audioState.lastPlayed.isPlaying = false
	}()

	if err := playAudioFile(voiceConnection, path, *audioState.Player); err != nil {
		return fmt.Errorf("error playing audio file: %w", err)
	}

	if err := voiceConnection.Speaking(false); err != nil {
		return fmt.Errorf("error setting speaking state to false: %w", err)
	}

	return nil
}

// CheckDisconnectTimer starts a goroutine that checks if the bot should disconnect from the voice channel.
func CheckDisconnectTimer(s *discordgo.Session, audioState *AudioState, guildID string, disconnectTimeOut time.Duration) {
	go func() {
		for {
			if time.Since(audioState.lastPlayed.lastPlayedTime) > disconnectTimeOut && !audioState.lastPlayed.isPlaying && audioState.lastPlayed.shouldCheckDisconnect {
				if err := LeaveVoice(s, guildID); err != nil {
					fmt.Printf("Error leaving voice channel: %v\n", err)
				}
				audioState.lastPlayed.shouldCheckDisconnect = false
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

// GetUsersInVoiceChannels returns a list of user IDs and their corresponding voice channel IDs for a given guild.
func GetUsersInVoiceChannels(session *discordgo.Session, guildID string) ([]map[string]string, error) {
	guild, err := session.State.Guild(guildID)
	if err != nil {
		return nil, fmt.Errorf("error getting guild: %w", err)
	}

	var usersInVoiceChannels []map[string]string

	for _, voiceState := range guild.VoiceStates {
		userChannelPair := map[string]string{
			"userID":    voiceState.UserID,
			"channelID": voiceState.ChannelID,
		}

		usersInVoiceChannels = append(usersInVoiceChannels, userChannelPair)
	}

	return usersInVoiceChannels, nil
}

// IsChannelEmpty checks if a voice channel is empty.
func IsChannelEmpty(s *discordgo.Session, guildID, channelID string) (bool, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		return true, fmt.Errorf("error getting guild: %w", err)
	}

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == channelID {
			return false, nil
		}
	}
	return true, nil
}

// SaveChannelAudio joins a voice channel and saves audio from it.
func SaveChannelAudio(s *discordgo.Session, guildID, channelID string, silenceThreshold time.Duration, saveLocation string) error {
	voiceConnection, err := s.ChannelVoiceJoin(guildID, channelID, true, false)
	if err != nil {
		return fmt.Errorf("error joining voice channel: %w", err)
	}

	voiceConnection.AddHandler(populateSSRCMap)

	defer func() {
		if err := voiceConnection.Disconnect(); err != nil {
			fmt.Printf("Error disconnecting from voice channel: %v\n", err)
		}
		voiceConnection.Close()

		time.Sleep(1 * time.Second)
	}()

	pcmChan := make(chan *discordgo.Packet, 100)

	go receivePCM(voiceConnection, pcmChan)

	return saveAudioBySpeaker(pcmChan, silenceThreshold, saveLocation)
}
