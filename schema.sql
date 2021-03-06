begin;

create table issues(j jsonb not null, check(j?'id'));
create unique index issues_id_idx on issues(((j->>'id')::int));

create table comments(j jsonb not null, repo text not null, check(j?'id'));
create unique index comments_id_idx on comments(((j->>'id')::int));
create index comments_reactions_total_count_idx on comments (((j#>>'{reactions,total_count}')::int));
create index comments_user_login_idx on comments ((j#>>'{user,login}'));
create index comments_issue_url_idx on comments ((j->>'issue_url'));
create index comments_repo on comments (repo);

commit;