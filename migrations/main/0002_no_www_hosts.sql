-- www.<domain> is never a host: it always 301-redirects to <domain> (rule in the router).
-- Remove www rows that only duplicate the bare domain; turn the others into the bare domain.
DELETE FROM domains WHERE domain LIKE 'www.%' AND EXISTS (
    SELECT 1 FROM domains d2 WHERE d2.domain = substr(domains.domain, 5) AND d2.path_prefix = domains.path_prefix);
UPDATE OR IGNORE domains SET domain = substr(domain, 5) WHERE domain LIKE 'www.%';
DELETE FROM domains WHERE domain LIKE 'www.%';
