# DiscordGo Audio

This package is a wrapper on top of [DiscordGo](https://github.com/bwmarrin/discordgo) thast provides an easier to use interface for playing, listening, and controlling audio in a Discord voice channel. This package provides an AudioPlayer interface and handles AudioPlayer state accross servers. It also provides helpful functions for interacting with Discord voice channels. This package only supports one playing instance per server.

## Getting Started

### Prerequisites

- You must have ffmpeg in your path and Opus libs already installed

### Installation

This assumes you already have a working Go environment, if not please see
[this page](https://golang.org/doc/install) first.

```sh
go get github.com/bafifi/discordgoaudio
```

Import the package into your project

```
go import "github.com/bafifi/discordgoaudio"
```

## Example Usage

### AudioPlayer
Functionality to control audio playback
```go
func skip(s *discordgo.Session, guildId string) {
    state := discordgoaudio.GetServerAudioState(m.GuildID)
	if state.Player.StopAudioChannel != nil {
		state.Player.StopAudioChannel <- true
	}
}

func pause(s *discordgo.Session, guildId string) {
    state := discordgoaudio.GetServerAudioState(m.GuildID)
	if state.Player.PauseAudioChannel != nil {
		state.Player.PauseAudioChannel <- true
	}
}

func resume(s *discordgo.Session, guildId string) {
    state := discordgoaudio.GetServerAudioState(m.GuildID)
	if state.Player.ResumeAudioChannel != nil {
		state.Player.ResumeAudioChannel <- true
	}
}
```

### Record Voice Channel Audio
Starts Recording Discord Audio 
```go
func saveAudio(s *discordgo.Session, guildID, channelID string) {
    saveLocation := "some/path/here/" // path the audio is saved to. each user will have their own file in this dir
    silenceThreshold := 2 * time.Minute // time the user is silent before thier audio is saved
	discordgoaudio.SaveChannelAudio(s, guildID, silenceThreshold, saveLocation)
}
```

### Automatic Disconnect 
Disconnect from voice timer (Run Once at Start)
```go
discordgoaudio.CheckDisconnectTimer(session, discordgoaudio.GetServerAudioState(guildID), guildID, 5 * time.Minute)
```

### Other Discord Voice Helper Function

- FindUserVoiceChannel 
- JoinChannel
- GetUsersInVoiceChannels
- IsChannelEmpty
