package services

import (
	"github.com/muety/wakapi/config"
	"log"
	"runtime"
	"time"

	"github.com/jasonlvhit/gocron"
	"github.com/muety/wakapi/models"
)

const (
	aggregateIntervalDays int = 1
)

type AggregationService struct {
	config           *config.Config
	userService      *UserService
	summaryService   *SummaryService
	heartbeatService *HeartbeatService
}

func NewAggregationService(userService *UserService, summaryService *SummaryService, heartbeatService *HeartbeatService) *AggregationService {
	return &AggregationService{
		config:           config.Get(),
		userService:      userService,
		summaryService:   summaryService,
		heartbeatService: heartbeatService,
	}
}

type AggregationJob struct {
	UserID string
	From   time.Time
	To     time.Time
}

// Schedule a job to (re-)generate summaries every day shortly after midnight
func (srv *AggregationService) Schedule() {
	jobs := make(chan *AggregationJob)
	summaries := make(chan *models.Summary)
	defer close(jobs)
	defer close(summaries)

	for i := 0; i < runtime.NumCPU(); i++ {
		go srv.summaryWorker(jobs, summaries)
	}

	for i := 0; i < int(srv.config.Db.MaxConn); i++ {
		go srv.persistWorker(summaries)
	}

	// Run once initially
	srv.trigger(jobs)

	gocron.Every(1).Day().At(srv.config.App.AggregationTime).Do(srv.trigger, jobs)
	<-gocron.Start()
}

func (srv *AggregationService) summaryWorker(jobs <-chan *AggregationJob, summaries chan<- *models.Summary) {
	for job := range jobs {
		if summary, err := srv.summaryService.Construct(job.From, job.To, &models.User{ID: job.UserID}, true); err != nil {
			log.Printf("Failed to generate summary (%v, %v, %s) – %v.\n", job.From, job.To, job.UserID, err)
		} else {
			log.Printf("Successfully generated summary (%v, %v, %s).\n", job.From, job.To, job.UserID)
			summaries <- summary
		}
	}
}

func (srv *AggregationService) persistWorker(summaries <-chan *models.Summary) {
	for summary := range summaries {
		if err := srv.summaryService.Insert(summary); err != nil {
			log.Printf("Failed to save summary (%v, %v, %s) – %v.\n", summary.UserID, summary.FromTime, summary.ToTime, err)
		}
	}
}

func (srv *AggregationService) trigger(jobs chan<- *AggregationJob) error {
	log.Println("Generating summaries.")

	users, err := srv.userService.GetAll()
	if err != nil {
		log.Println(err)
		return err
	}

	latestSummaries, err := srv.summaryService.GetLatestByUser()
	if err != nil {
		log.Println(err)
		return err
	}

	userSummaryTimes := make(map[string]time.Time)
	for _, s := range latestSummaries {
		userSummaryTimes[s.UserID] = s.ToTime.T()
	}

	missingUserIDs := make([]string, 0)
	for _, u := range users {
		if _, ok := userSummaryTimes[u.ID]; !ok {
			missingUserIDs = append(missingUserIDs, u.ID)
		}
	}

	firstHeartbeats, err := srv.heartbeatService.GetFirstUserHeartbeats(missingUserIDs)
	if err != nil {
		log.Println(err)
		return err
	}

	for id, t := range userSummaryTimes {
		generateUserJobs(id, t, jobs)
	}

	for _, h := range firstHeartbeats {
		generateUserJobs(h.UserID, time.Time(h.Time), jobs)
	}

	return nil
}

func generateUserJobs(userId string, lastAggregation time.Time, jobs chan<- *AggregationJob) {
	var from, to time.Time
	end := getStartOfToday().Add(-1 * time.Second)

	if lastAggregation.Hour() == 0 {
		from = lastAggregation
	} else {
		from = time.Date(
			lastAggregation.Year(),
			lastAggregation.Month(),
			lastAggregation.Day()+aggregateIntervalDays,
			0, 0, 0, 0,
			lastAggregation.Location(),
		)
	}

	for from.Before(end) && to.Before(end) {
		to = time.Date(
			from.Year(),
			from.Month(),
			from.Day()+aggregateIntervalDays,
			0, 0, 0, 0,
			from.Location(),
		)
		jobs <- &AggregationJob{userId, from, to}
		from = to
	}
}

func getStartOfToday() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 1, now.Location())
}
