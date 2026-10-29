package livechat

import (
	"context"
	"testing"
	"time"

	"livechat/pkg/provider"
)

func TestLibStreamerForwardsMessages(t *testing.T) {
	s := &libStreamer{
		kind:   KindYouTubeNoAuth,
		target: "https://youtu.be/x",
		start: func(ctx context.Context, target string) (<-chan provider.ChatMessage, <-chan error, error) {
			msgs := make(chan provider.ChatMessage, 1)
			errs := make(chan error, 1)
			msgs <- provider.ChatMessage{Platform: "youtube", Author: "a", Text: "hi", TS: time.Unix(1, 0)}
			close(msgs)
			return msgs, errs, nil
		},
	}
	var got []Message
	if err := s.Stream(context.Background(), func(m Message) { got = append(got, m) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].User != "a" || got[0].Text != "hi" || got[0].Source != KindYouTubeNoAuth {
		t.Fatalf("got %+v", got)
	}
}

func TestBiliLibStreamerRejectsIncompleteCookie(t *testing.T) {
	s := biliLibStreamer(SourceConfig{Channel: "6", BilibiliCookie: "SESSDATA=only"})
	err := s.Stream(context.Background(), func(Message) {})
	if err == nil {
		t.Fatal("残缺 Cookie 应直接失败")
	}
}
