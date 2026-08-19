local v = redis.call('GET', KEYS[1])
if not v then return false end
if redis.call('HGET', KEYS[2], ARGV[1]) ~= ARGV[2] then return false end
return v
