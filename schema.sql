begin;

create table issues(j jsonb not null, check(j?'id'));
create unique index on issues(((j->>'id')::int));

create table comments(j jsonb not null, check(j?'id'));
create unique index on comments(((j->>'id')::int));
create index on comments (((j#>>'{reactions,total_count}')::int));
create index on comments ((j#>>'{user,login}'));
create index on comments ((j->>'issue_url'));

create table jobs(id bigserial primary key, type text not null, payload jsonb not null unique, created_at timestamp with time zone default current_timestamp);

create function  notify_jobs()
  returns trigger
  as $$
  begin
    notify jobs;
    return null;
  end;
$$ language plpgsql;

create trigger notify_jobs after insert
  on jobs
execute procedure notify_jobs();

commit;