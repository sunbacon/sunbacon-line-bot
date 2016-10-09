package bot

import (
	"strings"
	"encoding/base64"
	"encoding/json"
	"github.com/joho/godotenv"
	"github.com/line/line-bot-sdk-go/linebot"
	"google.golang.org/appengine"
	"google.golang.org/appengine/taskqueue"
	"google.golang.org/appengine/urlfetch"
	"google.golang.org/appengine/log"
	"net/http"
	"os"
	"google.golang.org/api/translate/v2"
    "net/url"
	"google.golang.org/api/vision/v1"
	"golang.org/x/oauth2/google"
	"golang.org/x/net/context"
	"io/ioutil"
	"time"
	"math/rand"
	"fmt"
	"google.golang.org/cloud/storage"
	"google.golang.org/appengine/file"
	"google.golang.org/api/googleapi/transport"
)

var channelSecret, channelToken, translateApiKey string

func init() {
	err := godotenv.Load("line.env")
	if err != nil {
		panic(err)
	}
    channelSecret = os.Getenv("CHANNEL_SECRET")
    channelToken = os.Getenv("CHANNEL_TOKEN")
	apiKey = os.Getenv("TRANSLATE_API_KEY")
	http.HandleFunc("/message", handleMessage)
	http.HandleFunc("/task", handleTask)
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	bot, err := linebot.New(
		channelSecret,
		channelToken,
		linebot.WithHTTPClient(urlfetch.Client(c)))
	if err != nil {
		log.Errorf(c, "new linebot:%v", err)
	}

	events, err := bot.ParseRequest(r)
	if err != nil {
		log.Errorf(c, "parse:%v", err)
	}

	tasks := make([]*taskqueue.Task, len(events))
	for i, event := range events {
		j, err := json.Marshal(event)
		if err != nil {
			log.Errorf(c, "marshal:%v", err)
		}
		data := base64.StdEncoding.EncodeToString(j)
		task := taskqueue.NewPOSTTask("/task", url.Values{"data":{data}})
		tasks[i] = task
	}
	taskqueue.AddMulti(c, tasks, "")
	w.WriteHeader(204)
}

func handleTask(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	data := r.FormValue("data")
	if data == "" {
		log.Errorf(c, "no data")
	}

	j, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		log.Errorf(c, "decode :%v", err)
	}

	event := new(linebot.Event)
	err = json.Unmarshal(j, event)
	if err != nil {
		log.Errorf(c, "Unmarshal:%v", err)
	}

	bot, err := linebot.New(
		channelSecret,
		channelToken,
		linebot.WithHTTPClient(urlfetch.Client(c)))
	if err != nil {
		log.Errorf(c, "new linebot:%v", err)
	}

	source := event.Source

	switch source.Type {
	case linebot.EventSourceTypeUser:

		switch event.Type {
		case linebot.EventTypeFollow:
			m := linebot.NewTextMessage("フォローありがとう！")
			if _, err = bot.ReplyMessage(event.ReplyToken, m).WithContext(c).Do(); err != nil {
				log.Errorf(c, "reply:%v", err)
			}
		case linebot.EventTypeMessage:
			switch message := event.Message.(type) {
			case *linebot.TextMessage:
				m := linebot.NewTextMessage(GetRandomText())
				if _, err = bot.ReplyMessage(event.ReplyToken, m).WithContext(c).Do(); err != nil {
					log.Errorf(c, "reply:%v", err)
				}
			case *linebot.ImageMessage:

				content, err := bot.GetMessageContent(message.ID).WithContext(c).Do()
				if err != nil {
					log.Errorf(c, "get content:%v", err)
				}

				var imgByte []byte
				imgByte, err = ioutil.ReadAll(content.Content)
				if err != nil {
					log.Errorf(c, "read:%v", err)
				}

				if err = saveImage(c, imgByte); err != nil {
					log.Errorf(c, "save image:%v", err)
				}

				visionResult, err := requestVisionApi(c, imgByte)
				if err != nil {
					log.Errorf(c, "vision Api:%v", err)
				}

				translatedStr, err := requestTranslateApi(c, visionResult)
				if err != nil {
					log.Errorf(c, "translate:%v", err)
				}

				m := linebot.NewTextMessage(translatedStr)

				if _, err = bot.ReplyMessage(event.ReplyToken, m).WithContext(c).Do(); err != nil {
					log.Errorf(c, "reply:%v", err)
				}
			default:
			}
		}

	default:
		// なんもしない
	}
	w.WriteHeader(200)
}

func requestTranslateApi(c context.Context,source []string)(string, error) {
	service, err := translate.New(&http.Client{
		Transport: &transport.APIKey{
			Key: translateApiKey,
			Transport: &urlfetch.Transport{Context: c},
		},
	})
	if err != nil {
		return "", err
	}

	response, err := service.Translations.List(source,"ja").Context(c).Do()
	if err != nil {
		return "", err
	}

	// ひとつの文字列に整形
	s := ""
	for _, translation := range response.Translations {
		s = s + translation.TranslatedText + "\n"
	}
	s = strings.TrimRight(s, "\n")
	return s, nil
}

func requestVisionApi(c context.Context,imgData []byte) ([]string ,error) {
	enc := base64.StdEncoding.EncodeToString(imgData)
	img := &vision.Image{Content: enc}
	feature := &vision.Feature{
		Type:       "LABEL_DETECTION",
		MaxResults: 10,
	}

	req := &vision.AnnotateImageRequest{
		Image:    img,
		Features: []*vision.Feature{feature},
	}

	batch := &vision.BatchAnnotateImagesRequest{
		Requests: []*vision.AnnotateImageRequest{req},
	}

	client, err := google.DefaultClient(c, vision.CloudPlatformScope)
	if err != nil {
		return nil, err
	}

	service, err := vision.New(client)

	res, err := service.Images.Annotate(batch).Do()
	if err != nil {
		return nil, err
	}

	// Description : Score（％）
	// の形に整形
	result := []string{}
	for _,annotation := range res.Responses[0].LabelAnnotations {
		result = append(result, fmt.Sprintf("%s:%d％",annotation.Description, int(annotation.Score * 100.0)))
	}
	return result, nil
}

func saveImage(c context.Context,imgData []byte) error{
	bucketName, err := file.DefaultBucketName(c)
	objectName := fmt.Sprintf("s%d%d.jpg",time.Now().Unix(),rand.Intn(100000))

	if err != nil {
		return err
	}
	client, err := storage.NewClient(c)
	if err != nil {
		return err
	}

	writer := client.Bucket(bucketName).Object(objectName).NewWriter(c)
	writer.ContentType = "image/jpeg"
	defer writer.Close()

	if _, err := writer.Write(imgData); err != nil {
		return err
	}
	return nil
}

func GetRandomText() string {
	rand.Seed(time.Now().UnixNano())
	i := rand.Intn(100)
	switch i % 20 {
		case 0:
		return "うーん"
		case 1:
		return "つらい"
		case 2:
		return "わかる"
		case 3:
		return "わからず"
		case 4:
		return "そんなことないよ"
		case 5:
		return "かわいい"
		case 6:
		return "つよそう"
		case 7:
		return "それ"
		case 8:
		return "すてき"
		case 9:
		return "すき"
		case 10:
		return "無職"
		case 11:
		return "なんでやねん"
		case 12:
		return "優勝！！！！"
		case 13:
		return "準優勝"
		case 14:
		return "ビール"
		case 15:
		return "ボドゲ"
		case 16:
		return "とは"
		case 17:
		return "わからずの森"
		case 18:
		return "しずかに"
		case 19:
		return "にゃーん"
	}
	return "えええええ"
}
