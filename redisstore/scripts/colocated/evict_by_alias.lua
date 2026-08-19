local pk = redis.call('GET', KEYS[1])
if not pk then return 0 end
redis.call('DEL', ARGV[1] .. pk)
local mkey = ARGV[2] .. pk
local all = redis.call('HGETALL', mkey)
for i = 1, #all, 2 do
  redis.call('DEL', ARGV[3] .. all[i] .. ':' .. all[i + 1])
end
redis.call('DEL', mkey)
return 1
