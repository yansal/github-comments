create table issues(j jsonb not null, check(j?'id'));
create unique index on issues(((j->>'id')::int));

create table comments(j jsonb not null, check(j?'id'));
create unique index on comments(((j->>'id')::int));
create index on comments (((j#>>'{reactions,total_count}')::int));
create index on comments ((j#>>'{user,login}'));
create index on comments ((j->>'issue_url'));

create table users(login text primary key, created_at timestamp with time zone default current_timestamp);

-- notify worker after insert
create function
  notify_users()
  returns trigger
  as $$
  begin
    notify users;
    return null;
  end;
$$ language plpgsql;

create trigger notify_users after insert
  on users
execute procedure notify_users();