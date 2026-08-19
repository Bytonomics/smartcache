redis.call('DEL', KEYS[1])
local all = redis.call('HGETALL', KEYS[2])
for i = 1, #all, 2 do
  redis.call('DEL', ARGV[1] .. all[i] .. ':' .. all[i + 1])
end
redis.call('DEL', KEYS[2])
return 1
