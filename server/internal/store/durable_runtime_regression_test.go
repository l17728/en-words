package store

import (
 "errors"
 "path/filepath"
 "testing"
 "time"
 "enwords/internal/config"
 "enwords/internal/memory"
 "gorm.io/gorm"
 "github.com/stretchr/testify/require"
)

func reopenableFixture(t *testing.T)(func()*gorm.DB){
 t.Helper();path:=filepath.Join(t.TempDir(),"durable-runtime.db")
 return func()*gorm.DB{db,err:=Open(path);require.NoError(t,err);sql,err:=db.DB();require.NoError(t,err);t.Cleanup(func(){_=sql.Close()});return db}
}

func TestDurableMemoryRevisionIsolationAndReopen(t *testing.T){
 open:=reopenableFixture(t);db:=open();repo:=NewMemoryRepo(db)
 original:=&memory.Memory{OwnerRole:memory.OwnerTeacher,StudentID:7,BookID:3,Kind:"mistake",Content:"first",Importance:3,SourceLessonID:12,CreatedAt:time.Now()}
 require.NoError(t,repo.Append(original));require.Positive(t,original.ID)
 for _,m:=range []*memory.Memory{
  {OwnerRole:memory.OwnerSystem,StudentID:7,BookID:3,Content:"different owner"},
  {OwnerRole:memory.OwnerTeacher,StudentID:8,BookID:3,Content:"different student"},
  {OwnerRole:memory.OwnerTeacher,StudentID:7,BookID:4,Content:"different book"},
 }{require.NoError(t,repo.Append(m))}
 revised:=&memory.Memory{OwnerRole:memory.OwnerTeacher,StudentID:7,BookID:3,Kind:"mistake",Content:"revised",Importance:4,SourceLessonID:13,RevisedFrom:original.ID}
 require.NoError(t,repo.Append(revised))
 revised.Content="updated revision";require.NoError(t,repo.Update(revised))
 sql,err:=db.DB();require.NoError(t,err);require.NoError(t,sql.Close())
 reopened:=NewMemoryRepo(open())
 rows,err:=reopened.List(memory.OwnerTeacher,7,3);require.NoError(t,err);require.Len(t,rows,2)
 require.Equal(t,original.ID,rows[0].ID);require.Equal(t,revised.ID,rows[1].ID)
 got,err:=reopened.Get(revised.ID);require.NoError(t,err)
 require.Equal(t,"updated revision",got.Content);require.Equal(t,original.ID,got.RevisedFrom);require.EqualValues(t,13,got.SourceLessonID)
 require.NoError(t,reopened.Delete(revised.ID))
 _,err=reopened.Get(revised.ID);require.ErrorIs(t,err,memory.ErrNotFound)
 rows,err=reopened.List(memory.OwnerTeacher,7,3);require.NoError(t,err);require.Len(t,rows,1)
}

func TestDurableConfigGlobalOverrideRemovalSurvivesReopen(t *testing.T){
 open:=reopenableFixture(t);db:=open();p:=NewConfigPersister(db)
 require.NoError(t,p.SaveGlobal("wordsPerLesson",60))
 require.NoError(t,p.SaveOverride(7,"wordsPerLesson",80))
 require.NoError(t,p.SaveOverride(8,"wordsPerLesson",90))
 cfg,err:=config.NewStore(NewConfigPersister(open()),nil);require.NoError(t,err)
 require.Equal(t,60,cfg.Int(9,"wordsPerLesson"));require.Equal(t,80,cfg.Int(7,"wordsPerLesson"))
 require.NoError(t,p.SaveGlobal("wordsPerLesson",70));require.NoError(t,p.DeleteOverride(7,"wordsPerLesson"))
 restored,err:=config.NewStore(NewConfigPersister(open()),nil);require.NoError(t,err)
 require.Equal(t,70,restored.Int(7,"wordsPerLesson"));require.Equal(t,90,restored.Int(8,"wordsPerLesson"))
}

func TestDurableMemoryClosedDatabaseIsNotMissingRecord(t *testing.T){
 open:=reopenableFixture(t);db:=open();repo:=NewMemoryRepo(db)
 _,err:=repo.Get(999);require.ErrorIs(t,err,memory.ErrNotFound)
 sql,err:=db.DB();require.NoError(t,err);require.NoError(t,sql.Close())
 _,err=repo.Get(999);require.Error(t,err)
 require.False(t,errors.Is(err,memory.ErrNotFound),"storage outage must not become a missing memory")
}
