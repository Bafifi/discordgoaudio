package discordgoaudio

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/hraban/opus"
	"github.com/youpy/go-wav"
)

func populateSSRCMap(vc *discordgo.VoiceConnection, vs *discordgo.VoiceSpeakingUpdate) {
	ssrcToUserID.Lock()
	ssrcToUserID.ssrcMap[vs.SSRC] = vs.UserID
	ssrcToUserID.Unlock()
}

type speakerInfo struct {
	Decoder *opus.Decoder
	PCM     []int16
}

// audioPlayer represents a structure to control audio playback.
type audioPlayer struct {
	StopAudioChannel   chan bool
	PauseAudioChannel  chan bool
	ResumeAudioChannel chan bool
}

// createNewAudioPlayer creates and returns a new audioPlayer instance.
func createNewAudioPlayer() *audioPlayer {
	return &audioPlayer{
		StopAudioChannel:   make(chan bool),
		PauseAudioChannel:  make(chan bool),
		ResumeAudioChannel: make(chan bool),
	}
}

const (
	channels   int = 2
	sampleRate int = 48000
	frameSize  int = 960
	maxBytes   int = (frameSize * 2) * 2
)

var (
	opusEncoder *opus.Encoder
	speakers    map[uint32]speakerInfo
)

var ssrcToUserID = struct {
	sync.RWMutex
	ssrcMap map[int]string
}{ssrcMap: make(map[int]string)}

func init() {
	speakers = make(map[uint32]speakerInfo)
}

func sendPCM(v *discordgo.VoiceConnection, pcm <-chan []int16, player audioPlayer) error {
	if pcm == nil {
		return fmt.Errorf("PCM channel is nil")
	}

	var err error

	opusEncoder, err = opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		return fmt.Errorf("error creating new encoder: %w", err)
	}

	paused := false

	for {
		select {
		case <-player.PauseAudioChannel:
			paused = true
		case <-player.ResumeAudioChannel:
			paused = false
		case recv, ok := <-pcm:
			if !ok {
				return fmt.Errorf("PCM channel closed")
			}
			if paused {
				continue
			}

			opusBuf := make([]byte, maxBytes)
			n, err := opusEncoder.Encode(recv, opusBuf)
			if err != nil {
				return fmt.Errorf("error encoding audio: %w", err)
			}

			if !v.Ready || v.OpusSend == nil {
				return fmt.Errorf("discord connection not ready")
			}

			v.OpusSend <- opusBuf[:n]
		}
	}
}

func playAudioFile(v *discordgo.VoiceConnection, filename string, player audioPlayer) error {
	run := exec.Command("ffmpeg", "-i", filename, "-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", strconv.Itoa(channels), "pipe:1")
	ffmpegout, err := run.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating stdout pipe: %w", err)
	}

	ffmpegbuf := bufio.NewReaderSize(ffmpegout, 16384)

	err = run.Start()
	if err != nil {
		return fmt.Errorf("error starting ffmpeg: %w", err)
	}

	defer run.Process.Kill()

	err = v.Speaking(true)
	if err != nil {
		return fmt.Errorf("error setting speaking state: %w", err)
	}

	defer func() {
		err := v.Speaking(false)
		if err != nil {
			fmt.Printf("Error stopping speaking: %v\n", err)
		}
	}()

	send := make(chan []int16, 2)
	defer close(send)

	closeChan := make(chan bool)
	go func() {
		if err := sendPCM(v, send, player); err != nil {
			fmt.Printf("Error sending PCM: %v\n", err)
		}
		closeChan <- true
	}()

	for {
		audiobuf := make([]int16, frameSize*channels)
		err = binary.Read(ffmpegbuf, binary.LittleEndian, &audiobuf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("error reading from ffmpeg stdout: %w", err)
		}

		select {
		case send <- audiobuf:
		case <-closeChan:
			return nil
		case <-player.StopAudioChannel:
			run.Process.Kill()
			return nil
		}
	}
}

func receivePCM(v *discordgo.VoiceConnection, c chan *discordgo.Packet) error {
	if c == nil {
		return fmt.Errorf("channel is nil")
	}

	for {
		if !v.Ready || v.OpusRecv == nil {
			return fmt.Errorf("discordgo not ready to receive opus packets")
		}

		for user := range v.OpusRecv {
			_, ok := speakers[user.SSRC]
			if !ok {
				speakerDecoder, err := opus.NewDecoder(sampleRate, channels)
				if err != nil {
					return fmt.Errorf("error creating opus decoder: %w", err)
				}
				speakers[user.SSRC] = speakerInfo{speakerDecoder, make([]int16, frameSize*channels)}
			}

			n, err := speakers[user.SSRC].Decoder.Decode(user.Opus, speakers[user.SSRC].PCM)
			if err != nil {
				return fmt.Errorf("error decoding opus data: %w", err)
			}
			user.PCM = speakers[user.SSRC].PCM[:n*channels]

			c <- user
		}
	}
}

func saveAudioBySpeaker(c chan *discordgo.Packet, silenceThreshold time.Duration, saveLocation string) error {
	var pcmBuffers sync.Map
	var lastPacketTimes sync.Map

	for user := range c {
		userSSRC := user.SSRC

		// Initialize the PCM buffer and last packet time for the user
		buffer, _ := pcmBuffers.LoadOrStore(userSSRC, make([]int16, 0))
		pcmBuffer := buffer.([]int16)

		// Update the PCM buffer and last packet time
		pcmBuffer = append(pcmBuffer, user.PCM...)
		pcmBuffers.Store(userSSRC, pcmBuffer)
		lastPacketTimes.Store(userSSRC, time.Now())

		// If it's the first time we're seeing this user, start a goroutine for them
		go func(userSSRC uint32) {
			for {
				time.Sleep(silenceThreshold)
				lastPacketTimeIntf, ok := lastPacketTimes.Load(userSSRC)
				if !ok {
					continue
				}

				if time.Since(lastPacketTimeIntf.(time.Time)) >= silenceThreshold {
					// Retrieve and reset the PCM buffer for this user
					buffer, ok := pcmBuffers.Load(userSSRC)
					if !ok {
						continue
					}
					pcmBuffer := buffer.([]int16)
					if len(pcmBuffer) == 0 {
						continue
					}
					pcmBuffers.Store(userSSRC, make([]int16, 0))

					filePath := fmt.Sprintf("%s%s.wav", saveLocation, ssrcToUserID.ssrcMap[int(userSSRC)])
					if err := savePCMAsWAV(pcmBuffer, filePath); err != nil {
						fmt.Printf("Error saving PCM as WAV: %v\n", err)
						continue
					}
				}
			}
		}(userSSRC)
	}

	return nil
}

// savePCMAsWAV saves a PCM buffer as a WAV file.
func savePCMAsWAV(pcm []int16, filename string) error {
	outFile, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer outFile.Close()

	numSamples := len(pcm)
	buffer := make([]byte, numSamples*2)

	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(buffer[i*2:], uint16(sample))
	}

	writer := wav.NewWriter(outFile, uint32(numSamples/channels), uint16(channels), uint32(sampleRate), 16)

	_, err = writer.Write(buffer)
	if err != nil {
		return fmt.Errorf("error writing WAV data: %w", err)
	}

	return nil
}
