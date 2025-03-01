package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aler9/gortsplib"
	"github.com/aler9/gortsplib/pkg/base"
	"github.com/aler9/gortsplib/pkg/headers"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func mustParseURL(s string) *base.URL {
	u, err := base.ParseURL(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestClientRTSPPublishRead(t *testing.T) {
	for _, ca := range []struct {
		encrypted      bool
		publisherSoft  string
		publisherProto string
		readerSoft     string
		readerProto    string
	}{
		{false, "ffmpeg", "udp", "ffmpeg", "udp"},
		{false, "ffmpeg", "udp", "ffmpeg", "tcp"},
		{false, "ffmpeg", "udp", "gstreamer", "udp"},
		{false, "ffmpeg", "udp", "gstreamer", "tcp"},
		{false, "ffmpeg", "udp", "vlc", "udp"},
		{false, "ffmpeg", "udp", "vlc", "tcp"},

		{false, "ffmpeg", "tcp", "ffmpeg", "udp"},
		{false, "gstreamer", "udp", "ffmpeg", "udp"},
		{false, "gstreamer", "tcp", "ffmpeg", "udp"},

		{true, "ffmpeg", "tcp", "ffmpeg", "tcp"},
		{true, "ffmpeg", "tcp", "gstreamer", "tcp"},
		{true, "gstreamer", "tcp", "ffmpeg", "tcp"},
	} {
		encryptedStr := func() string {
			if ca.encrypted {
				return "encrypted"
			}
			return "plain"
		}()

		t.Run(encryptedStr+"_"+ca.publisherSoft+"_"+ca.publisherProto+"_"+
			ca.readerSoft+"_"+ca.readerProto, func(t *testing.T) {
			var proto string
			var port string
			if !ca.encrypted {
				proto = "rtsp"
				port = "8554"

				p, ok := testProgram("rtmpDisable: yes\n" +
					"hlsDisable: yes\n" +
					"readTimeout: 20s\n")
				require.Equal(t, true, ok)
				defer p.close()

			} else {
				proto = "rtsps"
				port = "8555"

				serverCertFpath, err := writeTempFile(serverCert)
				require.NoError(t, err)
				defer os.Remove(serverCertFpath)

				serverKeyFpath, err := writeTempFile(serverKey)
				require.NoError(t, err)
				defer os.Remove(serverKeyFpath)

				p, ok := testProgram("rtmpDisable: yes\n" +
					"hlsDisable: yes\n" +
					"readTimeout: 20s\n" +
					"protocols: [tcp]\n" +
					"encryption: yes\n" +
					"serverCert: " + serverCertFpath + "\n" +
					"serverKey: " + serverKeyFpath + "\n")
				require.Equal(t, true, ok)
				defer p.close()
			}

			switch ca.publisherSoft {
			case "ffmpeg":
				cnt1, err := newContainer("ffmpeg", "source", []string{
					"-re",
					"-stream_loop", "-1",
					"-i", "emptyvideo.mkv",
					"-c", "copy",
					"-f", "rtsp",
					"-rtsp_transport", ca.publisherProto,
					proto + "://" + ownDockerIP + ":" + port + "/teststream",
				})
				require.NoError(t, err)
				defer cnt1.close()

				time.Sleep(1 * time.Second)

			case "gstreamer":
				cnt1, err := newContainer("gstreamer", "source", []string{
					"filesrc location=emptyvideo.mkv ! matroskademux ! video/x-h264 ! rtspclientsink " +
						"location=" + proto + "://" + ownDockerIP + ":" + port + "/teststream " +
						"protocols=" + ca.publisherProto + " tls-validation-flags=0 latency=0 timeout=0 rtx-time=0",
				})
				require.NoError(t, err)
				defer cnt1.close()

				time.Sleep(1 * time.Second)
			}

			time.Sleep(1 * time.Second)

			switch ca.readerSoft {
			case "ffmpeg":
				cnt2, err := newContainer("ffmpeg", "dest", []string{
					"-rtsp_transport", ca.readerProto,
					"-i", proto + "://" + ownDockerIP + ":" + port + "/teststream",
					"-vframes", "1",
					"-f", "image2",
					"-y", "/dev/null",
				})
				require.NoError(t, err)
				defer cnt2.close()
				require.Equal(t, 0, cnt2.wait())

			case "gstreamer":
				cnt2, err := newContainer("gstreamer", "read", []string{
					"rtspsrc location=" + proto + "://" + ownDockerIP + ":" + port + "/teststream protocols=tcp tls-validation-flags=0 latency=0 " +
						"! application/x-rtp,media=video ! decodebin ! exitafterframe ! fakesink",
				})
				require.NoError(t, err)
				defer cnt2.close()
				require.Equal(t, 0, cnt2.wait())

			case "vlc":
				args := []string{}
				if ca.readerProto == "tcp" {
					args = append(args, "--rtsp-tcp")
				}
				args = append(args, proto+"://"+ownDockerIP+":"+port+"/teststream")
				cnt2, err := newContainer("vlc", "dest", args)
				require.NoError(t, err)
				defer cnt2.close()
				require.Equal(t, 0, cnt2.wait())
			}
		})
	}
}

func TestClientRTSPAuth(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		p, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"paths:\n" +
			"  all:\n" +
			"    publishUser: testuser\n" +
			"    publishPass: test!$()*+.;<=>[]^_-{}\n" +
			"    publishIps: [172.17.0.0/16]\n")
		require.Equal(t, true, ok)
		defer p.close()

		cnt1, err := newContainer("ffmpeg", "source", []string{
			"-re",
			"-stream_loop", "-1",
			"-i", "emptyvideo.mkv",
			"-c", "copy",
			"-f", "rtsp",
			"-rtsp_transport", "udp",
			"rtsp://testuser:test!$()*+.;<=>[]^_-{}@" + ownDockerIP + ":8554/test/stream",
		})
		require.NoError(t, err)
		defer cnt1.close()

		time.Sleep(1 * time.Second)

		cnt2, err := newContainer("ffmpeg", "dest", []string{
			"-rtsp_transport", "udp",
			"-i", "rtsp://" + ownDockerIP + ":8554/test/stream",
			"-vframes", "1",
			"-f", "image2",
			"-y", "/dev/null",
		})
		require.NoError(t, err)
		defer cnt2.close()
		require.Equal(t, 0, cnt2.wait())
	})

	for _, soft := range []string{
		"ffmpeg",
		"vlc",
	} {
		t.Run("read_"+soft, func(t *testing.T) {
			p, ok := testProgram("rtmpDisable: yes\n" +
				"hlsDisable: yes\n" +
				"paths:\n" +
				"  all:\n" +
				"    readUser: testuser\n" +
				"    readPass: test!$()*+.;<=>[]^_-{}\n" +
				"    readIps: [172.17.0.0/16]\n")
			require.Equal(t, true, ok)
			defer p.close()

			cnt1, err := newContainer("ffmpeg", "source", []string{
				"-re",
				"-stream_loop", "-1",
				"-i", "emptyvideo.mkv",
				"-c", "copy",
				"-f", "rtsp",
				"-rtsp_transport", "udp",
				"rtsp://" + ownDockerIP + ":8554/test/stream",
			})
			require.NoError(t, err)
			defer cnt1.close()

			time.Sleep(1 * time.Second)

			if soft == "ffmpeg" {
				cnt2, err := newContainer("ffmpeg", "dest", []string{
					"-rtsp_transport", "udp",
					"-i", "rtsp://testuser:test!$()*+.;<=>[]^_-{}@" + ownDockerIP + ":8554/test/stream",
					"-vframes", "1",
					"-f", "image2",
					"-y", "/dev/null",
				})
				require.NoError(t, err)
				defer cnt2.close()
				require.Equal(t, 0, cnt2.wait())

			} else {
				cnt2, err := newContainer("vlc", "dest", []string{
					"rtsp://testuser:test!$()*+.;<=>[]^_-{}@" + ownDockerIP + ":8554/test/stream",
				})
				require.NoError(t, err)
				defer cnt2.close()
				require.Equal(t, 0, cnt2.wait())
			}
		})
	}

	t.Run("hashed", func(t *testing.T) {
		p, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"paths:\n" +
			"  all:\n" +
			"    readUser: sha256:rl3rgi4NcZkpAEcacZnQ2VuOfJ0FxAqCRaKB/SwdZoQ=\n" +
			"    readPass: sha256:E9JJ8stBJ7QM+nV4ZoUCeHk/gU3tPFh/5YieiJp6n2w=\n")
		require.Equal(t, true, ok)
		defer p.close()

		cnt1, err := newContainer("ffmpeg", "source", []string{
			"-re",
			"-stream_loop", "-1",
			"-i", "emptyvideo.mkv",
			"-c", "copy",
			"-f", "rtsp",
			"-rtsp_transport", "udp",
			"rtsp://" + ownDockerIP + ":8554/test/stream",
		})
		require.NoError(t, err)
		defer cnt1.close()

		cnt2, err := newContainer("ffmpeg", "dest", []string{
			"-rtsp_transport", "udp",
			"-i", "rtsp://testuser:testpass@" + ownDockerIP + ":8554/test/stream",
			"-vframes", "1",
			"-f", "image2",
			"-y", "/dev/null",
		})
		require.NoError(t, err)
		defer cnt2.close()
		require.Equal(t, 0, cnt2.wait())
	})
}

func TestClientRTSPAuthFail(t *testing.T) {
	for _, ca := range []struct {
		name string
		user string
		pass string
	}{
		{
			"wronguser",
			"test1user",
			"testpass",
		},
		{
			"wrongpass",
			"testuser",
			"test1pass",
		},
		{
			"wrongboth",
			"test1user",
			"test1pass",
		},
	} {
		t.Run("publish_"+ca.name, func(t *testing.T) {
			p, ok := testProgram("rtmpDisable: yes\n" +
				"hlsDisable: yes\n" +
				"paths:\n" +
				"  all:\n" +
				"    publishUser: testuser\n" +
				"    publishPass: testpass\n")
			require.Equal(t, true, ok)
			defer p.close()

			track, err := gortsplib.NewTrackH264(68, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04})
			require.NoError(t, err)

			_, err = gortsplib.DialPublish(
				"rtsp://"+ca.user+":"+ca.pass+"@"+ownDockerIP+":8554/test/stream",
				gortsplib.Tracks{track},
			)
			require.Equal(t, "wrong status code: 401 (Unauthorized)", err.Error())
		})
	}

	for _, ca := range []struct {
		name string
		user string
		pass string
	}{
		{
			"wronguser",
			"test1user",
			"testpass",
		},
		{
			"wrongpass",
			"testuser",
			"test1pass",
		},
		{
			"wrongboth",
			"test1user",
			"test1pass",
		},
	} {
		t.Run("read_"+ca.name, func(t *testing.T) {
			p, ok := testProgram("rtmpDisable: yes\n" +
				"hlsDisable: yes\n" +
				"paths:\n" +
				"  all:\n" +
				"    readUser: testuser\n" +
				"    readPass: testpass\n")
			require.Equal(t, true, ok)
			defer p.close()

			_, err := gortsplib.DialRead(
				"rtsp://" + ca.user + ":" + ca.pass + "@" + ownDockerIP + ":8554/test/stream",
			)
			require.Equal(t, "wrong status code: 401 (Unauthorized)", err.Error())
		})
	}

	t.Run("ip", func(t *testing.T) {
		p, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"paths:\n" +
			"  all:\n" +
			"    publishIps: [127.0.0.1/32]\n")
		require.Equal(t, true, ok)
		defer p.close()

		track, err := gortsplib.NewTrackH264(68, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04})
		require.NoError(t, err)

		_, err = gortsplib.DialPublish(
			"rtsp://"+ownDockerIP+":8554/test/stream",
			gortsplib.Tracks{track},
		)
		require.Equal(t, "wrong status code: 401 (Unauthorized)", err.Error())
	})
}

func TestClientRTSPAutomaticProtocol(t *testing.T) {
	for _, source := range []string{
		"ffmpeg",
	} {
		t.Run(source, func(t *testing.T) {
			p, ok := testProgram("rtmpDisable: yes\n" +
				"hlsDisable: yes\n" +
				"protocols: [tcp]\n")
			require.Equal(t, true, ok)
			defer p.close()

			if source == "ffmpeg" {
				cnt1, err := newContainer("ffmpeg", "source", []string{
					"-re",
					"-stream_loop", "-1",
					"-i", "emptyvideo.mkv",
					"-c", "copy",
					"-f", "rtsp",
					"rtsp://" + ownDockerIP + ":8554/teststream",
				})
				require.NoError(t, err)
				defer cnt1.close()
			}

			cnt2, err := newContainer("ffmpeg", "dest", []string{
				"-i", "rtsp://" + ownDockerIP + ":8554/teststream",
				"-vframes", "1",
				"-f", "image2",
				"-y", "/dev/null",
			})
			require.NoError(t, err)
			defer cnt2.close()
			require.Equal(t, 0, cnt2.wait())
		})
	}
}

func TestClientRTSPPublisherOverride(t *testing.T) {
	for _, ca := range []string{
		"enabled",
		"disabled",
	} {
		t.Run(ca, func(t *testing.T) {
			conf := "rtmpDisable: yes\n" +
				"protocols: [tcp]\n"
			if ca == "disabled" {
				conf += "paths:\n" +
					"  all:\n" +
					"    disablePublisherOverride: yes\n"
			}
			p, ok := testProgram(conf)
			require.Equal(t, true, ok)
			defer p.close()

			track, err := gortsplib.NewTrackH264(68, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04})
			require.NoError(t, err)

			s1, err := gortsplib.DialPublish("rtsp://"+ownDockerIP+":8554/teststream",
				gortsplib.Tracks{track})
			require.NoError(t, err)
			defer s1.Close()

			s2, err := gortsplib.DialPublish("rtsp://"+ownDockerIP+":8554/teststream",
				gortsplib.Tracks{track})
			if ca == "enabled" {
				require.NoError(t, err)
				defer s2.Close()
			} else {
				require.Error(t, err)
			}

			d1, err := gortsplib.DialRead("rtsp://" + ownDockerIP + ":8554/teststream")
			require.NoError(t, err)
			defer d1.Close()

			readDone := make(chan struct{})
			frameRecv := make(chan struct{})
			go func() {
				defer close(readDone)
				d1.ReadFrames(func(trackID int, streamType base.StreamType, payload []byte) {
					if ca == "enabled" {
						require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, payload)
					} else {
						require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, payload)
					}
					close(frameRecv)
				})
			}()

			err = s1.WriteFrame(track.ID, gortsplib.StreamTypeRTP,
				[]byte{0x01, 0x02, 0x03, 0x04})
			if ca == "enabled" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if ca == "enabled" {
				err = s2.WriteFrame(track.ID, gortsplib.StreamTypeRTP,
					[]byte{0x05, 0x06, 0x07, 0x08})
				require.NoError(t, err)
			}

			<-frameRecv

			d1.Close()
			<-readDone
		})
	}
}

func TestClientRTSPNonCompliantFrameSize(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		p, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"readBufferSize: 4500\n")
		require.Equal(t, true, ok)
		defer p.close()

		track, err := gortsplib.NewTrackH264(96, []byte("123456"), []byte("123456"))
		require.NoError(t, err)

		client := &gortsplib.Client{
			StreamProtocol: func() *gortsplib.StreamProtocol {
				v := gortsplib.StreamProtocolTCP
				return &v
			}(),
			ReadBufferSize: 4500,
		}

		source, err := client.DialPublish("rtsp://"+ownDockerIP+":8554/teststream",
			gortsplib.Tracks{track})
		require.NoError(t, err)
		defer source.Close()

		dest, err := client.DialRead("rtsp://" + ownDockerIP + ":8554/teststream")
		require.NoError(t, err)
		defer dest.Close()

		input := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04, 0x05}, 4096/5)

		readDone := make(chan struct{})
		frameRecv := make(chan struct{})
		go func() {
			defer close(readDone)
			dest.ReadFrames(func(trackID int, streamType gortsplib.StreamType, payload []byte) {
				require.Equal(t, gortsplib.StreamTypeRTP, streamType)
				require.Equal(t, input, payload)
				close(frameRecv)
			})
		}()

		err = source.WriteFrame(track.ID, gortsplib.StreamTypeRTP, input)
		require.NoError(t, err)

		<-frameRecv

		dest.Close()
		<-readDone
	})

	t.Run("proxy", func(t *testing.T) {
		p1, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"protocols: [tcp]\n" +
			"readBufferSize: 4500\n")
		require.Equal(t, true, ok)
		defer p1.close()

		track, err := gortsplib.NewTrackH264(96, []byte("123456"), []byte("123456"))
		require.NoError(t, err)

		client := &gortsplib.Client{
			StreamProtocol: func() *gortsplib.StreamProtocol {
				v := gortsplib.StreamProtocolTCP
				return &v
			}(),
			ReadBufferSize: 4500,
		}

		source, err := client.DialPublish("rtsp://"+ownDockerIP+":8554/teststream",
			gortsplib.Tracks{track})
		require.NoError(t, err)
		defer source.Close()

		p2, ok := testProgram("rtmpDisable: yes\n" +
			"hlsDisable: yes\n" +
			"protocols: [tcp]\n" +
			"readBufferSize: 4500\n" +
			"rtspAddress: :8555\n" +
			"paths:\n" +
			"  teststream:\n" +
			"    source: rtsp://" + ownDockerIP + ":8554/teststream\n" +
			"    sourceProtocol: tcp\n")
		require.Equal(t, true, ok)
		defer p2.close()

		time.Sleep(100 * time.Millisecond)

		dest, err := client.DialRead("rtsp://" + ownDockerIP + ":8555/teststream")
		require.NoError(t, err)
		defer dest.Close()

		input := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04, 0x05}, 4096/5)

		readDone := make(chan struct{})
		frameRecv := make(chan struct{})
		go func() {
			defer close(readDone)
			dest.ReadFrames(func(trackID int, streamType gortsplib.StreamType, payload []byte) {
				require.Equal(t, gortsplib.StreamTypeRTP, streamType)
				require.Equal(t, input, payload)
				close(frameRecv)
			})
		}()

		err = source.WriteFrame(track.ID, gortsplib.StreamTypeRTP, input)
		require.NoError(t, err)

		<-frameRecv

		dest.Close()
		<-readDone
	})
}

func TestClientRTSPAdditionalInfos(t *testing.T) {
	getInfos := func() (*headers.RTPInfo, []*uint32, error) {
		u, err := base.ParseURL("rtsp://" + ownDockerIP + ":8554/teststream")
		if err != nil {
			return nil, nil, err
		}

		conn, err := gortsplib.Dial(u.Scheme, u.Host)
		if err != nil {
			return nil, nil, err
		}
		defer conn.Close()

		tracks, _, err := conn.Describe(u)
		if err != nil {
			return nil, nil, err
		}

		ssrcs := make([]*uint32, len(tracks))

		for i, t := range tracks {
			res, err := conn.Setup(headers.TransportModePlay, t, 0, 0)
			if err != nil {
				return nil, nil, err
			}

			var th headers.Transport
			err = th.Read(res.Header["Transport"])
			if err != nil {
				return nil, nil, err
			}

			ssrcs[i] = th.SSRC
		}

		res, err := conn.Play(nil)
		if err != nil {
			return nil, nil, err
		}

		var ri headers.RTPInfo
		err = ri.Read(res.Header["RTP-Info"])
		if err != nil {
			return nil, nil, err
		}

		return &ri, ssrcs, nil
	}

	p, ok := testProgram("rtmpDisable: yes\n" +
		"hlsDisable: yes\n")
	require.Equal(t, true, ok)
	defer p.close()

	track1, err := gortsplib.NewTrackH264(96, []byte("123456"), []byte("123456"))
	require.NoError(t, err)

	track2, err := gortsplib.NewTrackH264(96, []byte("123456"), []byte("123456"))
	require.NoError(t, err)

	source, err := gortsplib.DialPublish("rtsp://"+ownDockerIP+":8554/teststream",
		gortsplib.Tracks{track1, track2})
	require.NoError(t, err)
	defer source.Close()

	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        0x80,
			PayloadType:    96,
			SequenceNumber: 556,
			Timestamp:      984512368,
			SSRC:           96342362,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}

	buf, err := pkt.Marshal()
	require.NoError(t, err)

	err = source.WriteFrame(track1.ID, gortsplib.StreamTypeRTP, buf)
	require.NoError(t, err)

	rtpInfo, ssrcs, err := getInfos()
	require.NoError(t, err)
	require.Equal(t, &headers.RTPInfo{
		&headers.RTPInfoEntry{
			URL: (&base.URL{
				Scheme: "rtsp",
				Host:   ownDockerIP + ":8554",
				Path:   "/teststream/trackID=0",
			}).String(),
			SequenceNumber: func() *uint16 {
				v := uint16(556)
				return &v
			}(),
			Timestamp: (*rtpInfo)[0].Timestamp,
		},
	}, rtpInfo)
	require.Equal(t, []*uint32{
		func() *uint32 {
			v := uint32(96342362)
			return &v
		}(),
		nil,
	}, ssrcs)

	pkt = rtp.Packet{
		Header: rtp.Header{
			Version:        0x80,
			PayloadType:    96,
			SequenceNumber: 87,
			Timestamp:      756436454,
			SSRC:           536474323,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}

	buf, err = pkt.Marshal()
	require.NoError(t, err)

	err = source.WriteFrame(track2.ID, gortsplib.StreamTypeRTP, buf)
	require.NoError(t, err)

	rtpInfo, ssrcs, err = getInfos()
	require.NoError(t, err)
	require.Equal(t, &headers.RTPInfo{
		&headers.RTPInfoEntry{
			URL: (&base.URL{
				Scheme: "rtsp",
				Host:   ownDockerIP + ":8554",
				Path:   "/teststream/trackID=0",
			}).String(),
			SequenceNumber: func() *uint16 {
				v := uint16(556)
				return &v
			}(),
			Timestamp: (*rtpInfo)[0].Timestamp,
		},
		&headers.RTPInfoEntry{
			URL: (&base.URL{
				Scheme: "rtsp",
				Host:   ownDockerIP + ":8554",
				Path:   "/teststream/trackID=1",
			}).String(),
			SequenceNumber: func() *uint16 {
				v := uint16(87)
				return &v
			}(),
			Timestamp: (*rtpInfo)[1].Timestamp,
		},
	}, rtpInfo)
	require.Equal(t, []*uint32{
		func() *uint32 {
			v := uint32(96342362)
			return &v
		}(),
		func() *uint32 {
			v := uint32(536474323)
			return &v
		}(),
	}, ssrcs)
}

func TestClientRTSPRedirect(t *testing.T) {
	p1, ok := testProgram("rtmpDisable: yes\n" +
		"hlsDisable: yes\n" +
		"paths:\n" +
		"  path1:\n" +
		"    source: redirect\n" +
		"    sourceRedirect: rtsp://" + ownDockerIP + ":8554/path2\n" +
		"  path2:\n")
	require.Equal(t, true, ok)
	defer p1.close()

	cnt1, err := newContainer("ffmpeg", "source", []string{
		"-re",
		"-stream_loop", "-1",
		"-i", "emptyvideo.mkv",
		"-c", "copy",
		"-f", "rtsp",
		"-rtsp_transport", "udp",
		"rtsp://" + ownDockerIP + ":8554/path2",
	})
	require.NoError(t, err)
	defer cnt1.close()

	time.Sleep(1 * time.Second)

	cnt2, err := newContainer("ffmpeg", "dest", []string{
		"-rtsp_transport", "udp",
		"-i", "rtsp://" + ownDockerIP + ":8554/path1",
		"-vframes", "1",
		"-f", "image2",
		"-y", "/dev/null",
	})
	require.NoError(t, err)
	defer cnt2.close()
	require.Equal(t, 0, cnt2.wait())
}

func TestClientRTSPFallback(t *testing.T) {
	for _, ca := range []string{
		"absolute",
		"relative",
	} {
		t.Run(ca, func(t *testing.T) {
			val := func() string {
				if ca == "absolute" {
					return "rtsp://" + ownDockerIP + ":8554/path2"
				}
				return "/path2"
			}()

			p1, ok := testProgram("rtmpDisable: yes\n" +
				"hlsDisable: yes\n" +
				"paths:\n" +
				"  path1:\n" +
				"    fallback: " + val + "\n" +
				"  path2:\n")
			require.Equal(t, true, ok)
			defer p1.close()

			cnt1, err := newContainer("ffmpeg", "source", []string{
				"-re",
				"-stream_loop", "-1",
				"-i", "emptyvideo.mkv",
				"-c", "copy",
				"-f", "rtsp",
				"-rtsp_transport", "udp",
				"rtsp://" + ownDockerIP + ":8554/path2",
			})
			require.NoError(t, err)
			defer cnt1.close()

			time.Sleep(1 * time.Second)

			cnt2, err := newContainer("ffmpeg", "dest", []string{
				"-rtsp_transport", "udp",
				"-i", "rtsp://" + ownDockerIP + ":8554/path1",
				"-vframes", "1",
				"-f", "image2",
				"-y", "/dev/null",
			})
			require.NoError(t, err)
			defer cnt2.close()
			require.Equal(t, 0, cnt2.wait())
		})
	}
}

func TestClientRTSPRunOnDemand(t *testing.T) {
	doneFile := filepath.Join(os.TempDir(), "ondemand_done")
	onDemandFile, err := writeTempFile([]byte(fmt.Sprintf(`#!/bin/sh
trap 'touch %s; [ -z "$(jobs -p)" ] || kill $(jobs -p)' INT
ffmpeg -hide_banner -loglevel error -re -i testimages/ffmpeg/emptyvideo.mkv -c copy -f rtsp rtsp://localhost:$RTSP_PORT/$RTSP_PATH &
wait
`, doneFile)))
	require.NoError(t, err)
	defer os.Remove(onDemandFile)

	err = os.Chmod(onDemandFile, 0o755)
	require.NoError(t, err)

	t.Run("describe", func(t *testing.T) {
		defer os.Remove(doneFile)

		p1, ok := testProgram(fmt.Sprintf("rtmpDisable: yes\n"+
			"hlsDisable: yes\n"+
			"paths:\n"+
			"  all:\n"+
			"    runOnDemand: %s\n"+
			"    runOnDemandCloseAfter: 2s\n", onDemandFile))
		require.Equal(t, true, ok)
		defer p1.close()

		func() {
			conn, err := net.Dial("tcp", "127.0.0.1:8554")
			require.NoError(t, err)
			defer conn.Close()
			bconn := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

			err = base.Request{
				Method: base.Describe,
				URL:    mustParseURL("rtsp://localhost:8554/ondemand"),
				Header: base.Header{
					"CSeq": base.HeaderValue{"1"},
				},
			}.Write(bconn.Writer)
			require.NoError(t, err)

			var res base.Response
			err = res.Read(bconn.Reader)
			require.NoError(t, err)
			require.Equal(t, base.StatusOK, res.StatusCode)
		}()

		for {
			_, err := os.Stat(doneFile)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	t.Run("describe and setup", func(t *testing.T) {
		defer os.Remove(doneFile)

		p1, ok := testProgram(fmt.Sprintf("rtmpDisable: yes\n"+
			"hlsDisable: yes\n"+
			"paths:\n"+
			"  all:\n"+
			"    runOnDemand: %s\n"+
			"    runOnDemandCloseAfter: 2s\n", onDemandFile))
		require.Equal(t, true, ok)
		defer p1.close()

		func() {
			conn, err := net.Dial("tcp", "127.0.0.1:8554")
			require.NoError(t, err)
			defer conn.Close()
			bconn := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

			err = base.Request{
				Method: base.Describe,
				URL:    mustParseURL("rtsp://localhost:8554/ondemand"),
				Header: base.Header{
					"CSeq": base.HeaderValue{"1"},
				},
			}.Write(bconn.Writer)
			require.NoError(t, err)

			var res base.Response
			err = res.Read(bconn.Reader)
			require.NoError(t, err)
			require.Equal(t, base.StatusOK, res.StatusCode)

			err = base.Request{
				Method: base.Setup,
				URL:    mustParseURL("rtsp://localhost:8554/ondemand/trackID=0"),
				Header: base.Header{
					"CSeq": base.HeaderValue{"2"},
					"Transport": headers.Transport{
						Protocol: gortsplib.StreamProtocolTCP,
						Delivery: func() *base.StreamDelivery {
							v := base.StreamDeliveryUnicast
							return &v
						}(),
						Mode: func() *headers.TransportMode {
							v := headers.TransportModePlay
							return &v
						}(),
						InterleavedIDs: &[2]int{0, 1},
					}.Write(),
				},
			}.Write(bconn.Writer)
			require.NoError(t, err)

			err = res.Read(bconn.Reader)
			require.NoError(t, err)
			require.Equal(t, base.StatusOK, res.StatusCode)
		}()

		for {
			_, err := os.Stat(doneFile)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	t.Run("setup", func(t *testing.T) {
		defer os.Remove(doneFile)

		p1, ok := testProgram(fmt.Sprintf("rtmpDisable: yes\n"+
			"hlsDisable: yes\n"+
			"paths:\n"+
			"  all:\n"+
			"    runOnDemand: %s\n"+
			"    runOnDemandCloseAfter: 2s\n", onDemandFile))
		require.Equal(t, true, ok)
		defer p1.close()

		func() {
			conn, err := net.Dial("tcp", "127.0.0.1:8554")
			require.NoError(t, err)
			defer conn.Close()
			bconn := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

			err = base.Request{
				Method: base.Setup,
				URL:    mustParseURL("rtsp://localhost:8554/ondemand/trackID=0"),
				Header: base.Header{
					"CSeq": base.HeaderValue{"1"},
					"Transport": headers.Transport{
						Protocol: gortsplib.StreamProtocolTCP,
						Delivery: func() *base.StreamDelivery {
							v := base.StreamDeliveryUnicast
							return &v
						}(),
						Mode: func() *headers.TransportMode {
							v := headers.TransportModePlay
							return &v
						}(),
						InterleavedIDs: &[2]int{0, 1},
					}.Write(),
				},
			}.Write(bconn.Writer)
			require.NoError(t, err)

			var res base.Response
			err = res.Read(bconn.Reader)
			require.NoError(t, err)
			require.Equal(t, base.StatusOK, res.StatusCode)
		}()

		for {
			_, err := os.Stat(doneFile)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	})
}
