package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/alexflint/go-arg"
	"github.com/fatih/color"
	"github.com/schollz/progressbar/v3"
	"github.com/ssgelm/cookiejarparser"
)

const (
	API_BASE_URL        = "https://www.humblebundle.com/"
	API_PURCHASE_DETAIL = "https://www.humblebundle.com/api/v1/order/%s?all_tpkds=false"
	LIBARARY_PAGE       = "https://www.humblebundle.com/home/library"
)

var green = color.New(color.FgHiGreen).SprintfFunc()
var red = color.New(color.FgHiRed).SprintfFunc()
var yellow = color.New(color.FgHiYellow).SprintfFunc()

type HbdSession struct {
	cliArgs         *args
	cookieJar       http.CookieJar
	client          *http.Client
	userHomes       *hb_user_homes
	purchaseDetails map[string]*hb_purchase_detail
	downloadQueue   *downloadQueue
}

func main() {

	sess := HbdSession{
		cliArgs:         &args{},
		client:          &http.Client{},
		userHomes:       &hb_user_homes{},
		purchaseDetails: make(map[string]*hb_purchase_detail),
		downloadQueue: &downloadQueue{
			current:  0,
			elements: make([]*queueElement, 0),
		},
	}

	arg.MustParse(sess.cliArgs)

	if sess.cliArgs.NoColor {
		color.NoColor = true // disables colorized output
	}

	err := sess.processCookieJar()
	if err != nil {
		panic(fmt.Errorf(red("Error processing cookie jar: %s", err)))
	}

	if sess.cliArgs.List != nil {
		fmt.Println(yellow("List of your HumbleBundle purchases"))
		sess.ProcessList()
	} else if sess.cliArgs.Download != nil {
		fmt.Println(yellow("Download of HumbleBundle purchases %s", sess.cliArgs.Download.PurchaseKey))
		err := sess.DownloadPurchase(sess.cliArgs.Download.PurchaseKey)
		if err != nil {
			sess.errorLog("[E] error downloading purchase", err)
		}
	} else {
		panic("unknown command in argument")
	}
}

func (s *HbdSession) processCookieJar() error {
	cookies, err := cookiejarparser.LoadCookieJarFile(s.cliArgs.CookieJar)
	if err != nil {
		return err
	}
	s.cookieJar = cookies
	hbd_url, _ := url.Parse(API_BASE_URL)
	if err != nil {
		return err
	}
	for _, c := range s.cookieJar.Cookies(hbd_url) {
		if err := c.Valid(); err != nil {
			return err
		}
	}
	s.client.Jar = s.cookieJar
	return nil
}

func (s *HbdSession) GetBody(reqUrl string) (io.ReadCloser, error) {
	res, err := s.client.Get(reqUrl)
	if err != nil {
		panic(err)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("status code error: %d %s", res.StatusCode, res.Status)
	}
	return res.Body, nil
}

func (s *HbdSession) verboseLog(text string) {
	if s.cliArgs.Verbose {
		fmt.Println(yellow(text))
	}
}

func (s *HbdSession) resultLog(text string) {
	fmt.Println(green(text))
}

func (s *HbdSession) errorLog(text string, err error) {
	if s.cliArgs.Verbose {
		fmt.Printf("%s\n%v\n", red(text), err)
		fmt.Println(red("Exit due to error"))
		os.Exit(1)
	} else {
		panic(err)
	}
}

func (s *HbdSession) getGameKeys(body io.ReadCloser) error {
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return err
	}

	var embeddedJson string
	doc.Find("#user-home-json-data").Each(func(i int, sec *goquery.Selection) {
		// For each item found, get the title
		embeddedJson = sec.Nodes[0].FirstChild.Data
	})

	err = json.Unmarshal([]byte(embeddedJson), s.userHomes)
	return err
}

func (s *HbdSession) FetchDetails(gameKey string) (*hb_purchase_detail, error) {
	s.verboseLog(fmt.Sprintf("[X] Fetching details for %s", gameKey))
	url := fmt.Sprintf(API_PURCHASE_DETAIL, gameKey)

	body, err := s.GetBody(url)
	if err != nil {
		return nil, err
	}
	pdetail := &hb_purchase_detail{}
	err = json.NewDecoder(body).Decode(pdetail)
	if err != nil {
		return nil, err
	}

	return pdetail, nil
}

func (s *HbdSession) ProcessList() {
	s.verboseLog("[X] Fetch Purchase Library HTML to extract GameKeys")
	body, err := s.GetBody(LIBARARY_PAGE)

	if err != nil {
		s.errorLog("[E] Failed to fetch Purchase Library HTML", err)
	}
	defer body.Close()

	s.verboseLog("[X] ExtractGameKeys")
	if err := s.getGameKeys(body); err != nil {
		s.errorLog("[E] Could not extract gamekeys", err)
	}

	for _, v := range s.userHomes.Gamekeys {
		if s.cliArgs.List.PurchaseDetails {
			pd, err := s.FetchDetails(v)
			if err != nil {
				s.errorLog("[E] Could not fetch details", err)
			}
			s.purchaseDetails[v] = pd
		}
	}

	for _, v := range s.userHomes.Gamekeys {
		if s.cliArgs.List.PurchaseDetails {
			p := s.purchaseDetails[v]
			s.resultLog(fmt.Sprintf("%s\t%s", p.GameKey, p.Product.HumanName))
		} else {
			s.resultLog(fmt.Sprintf("\t %s", v))
		}
	}
}

func (s *HbdSession) DownloadPurchase(gameKey string) error {

	pd, err := s.FetchDetails(gameKey)
	if err != nil {
		return fmt.Errorf("[E] Could not fetch Details. %v", err)
	}
	s.verboseLog(fmt.Sprintf("[i] %s with %d elements", pd.Product.HumanName, len(pd.Subproducts)))

	libPath := s.cliArgs.Download.LibraryPath
	prodPath := path.Join(libPath, pd.Product.HumanName)
	err = os.Mkdir(prodPath, 0755)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("[E] Could not create directory %s: %v", prodPath, err)
	}

	for _, v := range pd.Subproducts {
		platforms := make([]string, 0)
		formats := make(map[string]bool)
		for _, d := range v.Downloads {
			platforms = append(platforms, d.Platform)
			for _, d := range d.DownloadStruct {
				if strings.ToLower(d.Name) == strings.ToLower(s.cliArgs.Download.Format) {
					s.addToQueue(v.HumanName, prodPath, strings.ToLower(d.Name), d)
				}
				formats[d.Name] = true
			}
		}
		usedFormats := make([]string, 0)
		for k, _ := range formats {
			usedFormats = append(usedFormats, k)
		}
		s.verboseLog(fmt.Sprintf("\t [i] %s\t%v\t%v", v.HumanName, platforms, usedFormats))
	}

	fmt.Printf("-----------------------\n\n")
	for _, v := range s.downloadQueue.elements {
		//fmt.Printf("\tprocess: %s, %d bytes --> (%s) \n", v.name, v.size, v.url)
		err := s.doDownloadWithProgressBar(v)
		if err != nil {
			s.errorLog("[E] Could not download item", err)
		}
	}
	return nil
}

func (s *HbdSession) addToQueue(name, destPath, ext string, info downloadStruct) {
	elem := queueElement{
		name:     name,
		progress: 0,
		url:      info.Url.Web,
		status:   "queued",
		size:     info.FileSize,
		destPath: destPath,
		ext:      ext,
	}
	s.downloadQueue.elements = append(s.downloadQueue.elements, &elem)
}

func (s *HbdSession) doDownloadWithProgressBar(v *queueElement) error {

	destFile := path.Join(v.destPath, fmt.Sprintf("%s.%s", v.name, v.ext))
	fi, err := os.Stat(destFile)
	if err != nil && os.IsNotExist(err) {
		v.status = "new file"
	} else if err != nil {
		return err
	}

	if fi != nil && fi.Size() == v.size {
		v.status = "skipped, file with same size and name exists"
		return nil
	}

	req, _ := http.NewRequest("GET", v.url, nil)
	resp, dnlErr := s.client.Do(req)
	if dnlErr != nil {
		return dnlErr
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("[E] Could not download %s. Status Code: %d", v.url, resp.StatusCode)
	}

	defer resp.Body.Close()
	s.verboseLog(fmt.Sprintf("[X] Downloading %s to %s\n", v.name, destFile))

	f, destErr := os.OpenFile(destFile, os.O_CREATE|os.O_WRONLY, 0644)
	if destErr != nil {
		return destErr
	}
	defer f.Close()

	bar := progressbar.DefaultBytes(
		resp.ContentLength,
		"downloading",
	)
	_, copyErr := io.Copy(io.MultiWriter(f, bar), resp.Body)
	return copyErr
}

// local STRUCTS //

type hb_user_homes struct {
	Gamekeys []string `json:"gamekeys"`
}

type hb_purchase_detail struct {
	AmountSpent float64 `json:"amount_spent"`
	Product     struct {
		HumanName string `json:"human_name"`
	} `json:"product"`
	GameKey     string `json:"gamekey"`
	CreatedAt   string `json:"created"`
	Subproducts []struct {
		HumanName   string `json:"human_name"`
		MachineName string `json:"machine_name"`
		Downloads   []struct {
			MachineName    string           `json:"machine_name"`
			Platform       string           `json:"platform"`
			DownloadStruct []downloadStruct `json:"download_struct"`
		} `json:"downloads"`
	} `json:"subproducts"`
	Payee struct {
		HumanName   string `json:"human_name"`
		MachineName string `json:"machine_name"`
	} `json:"payee"`
}

type downloadStruct struct {
	Sha1      string `json:"sha1"`
	Name      string `json:"name"`
	HumanSize string `json:"human_size"`
	FileSize  int64  `json:"file_size"`
	Url       struct {
		Bittorrent string `json:"bittorrent"`
		Web        string `json:"web"`
	} `json:"url"`
}

type args struct {
	List      *argListCommand     `arg:"subcommand:list"`
	Download  *argDownloadCommand `arg:"subcommand:download"`
	CookieJar string              `arg:"required,-c" help:"path to curl compatible cookie file"`
	NoColor   bool                `arg:"--nc" help:"Disable Colors in Console"`
	Verbose   bool                `arg:"-v" help:"Verbose Output"`
}

func (args) Description() string {
	bold := color.New(color.Bold).SprintFunc()
	return fmt.Sprintf("%s", bold("\nCLI Tool for your Humble Bundle Library. \nFocus on collections of Comics and eBooks\n"))
}

func (args) Epilogue() string {
	red := color.New(color.FgRed, color.Bold).SprintFunc()
	return fmt.Sprintf("%s \nhttps://github.com/xtream1101/humblebundle-downloader/blob/main/README.md", red("If you need help getting your HumbleBundle Cookies file check the Documentation at"))
}

type argListCommand struct {
	LibraryPath     string `arg:"-l" help:"path to library"`
	NoCache         bool   `arg:"--no-cache" help:"Does not read or write .cache.json"`
	PurchaseDetails bool   `arg:"--details" help:"Fetch details about purchase (slow)"`
}

type argDownloadCommand struct {
	Format      string `arg:"required,-f" help:"Choose Format. Usually ePub, Pdf, CBZ."`
	LibraryPath string `arg:"required,-l" help:"path to library"`
	NoCache     bool   `arg:"--no-cache" help:"Does not read or write .cache.json"`
	PurchaseKey string `arg:"required,-k" help:"Purchase Key for a collection of eBooks and Comics"`
}

type downloadQueue struct {
	current  int
	elements []*queueElement
}

type queueElement struct {
	size     int64
	url      string
	progress int
	name     string
	status   string
	destPath string
	ext      string
}
