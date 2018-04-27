begin;

create table issues(j jsonb not null, check(j?'id'));
create unique index on issues(((j->>'id')::int));

create table comments(j jsonb not null, check(j?'id'));
create unique index on comments(((j->>'id')::int));
create index on comments (((j#>>'{reactions,total_count}')::int));
create index on comments ((j#>>'{user,login}'));
create index on comments ((j->>'issue_url'));

create type fetch_type as enum ('issue', 'repo', 'user');
create table fetch_queue(
  id bigserial primary key,
  type fetch_type not null,
  payload jsonb not null unique,
  created_at timestamp with time zone default current_timestamp,
  retry int not null default 0
);

create function notify_fetcher()
  returns trigger
  as $$
  begin
    notify fetcher;
    return null;
  end;
$$ language plpgsql;

create trigger notify_fetcher after insert
  on fetch_queue
execute procedure notify_fetcher();

commit;